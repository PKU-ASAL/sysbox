package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

var operationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,128}$`)

// destroySyncTimeout bounds how long a destroy call waits for its run to reach a
// terminal status before returning. It is a var (not a const) so tests can
// shrink it.
var destroySyncTimeout = 2 * time.Minute

func validateOperationKey(key string) error {
	if !operationKeyPattern.MatchString(key) {
		return fmt.Errorf("operation key must be 1-128 ASCII letters, digits, colon, underscore, or hyphen")
	}
	return nil
}

type RunService struct {
	jobs            *Jobs
	scheduler       *SchedulerService
	hclFile         func(string) string
	stateManager    func(string) (*state.Manager, error)
	requiredForTopo func(string) ([]string, error)
	revisions       globalRevisionStore
	workspaces      *WorkspaceService
}

type RunStartRequest struct {
	Revision         string
	AgentID          string
	Target           string
	AllowUnsafeState bool
	Inputs           map[string]string
	DeadlineAt       *time.Time
}

type runServiceErrorKind string

const (
	runServiceBadRequest runServiceErrorKind = "bad_request"
	runServiceConflict   runServiceErrorKind = "conflict"
	runServiceNotFound   runServiceErrorKind = "not_found"
	runServiceInternal   runServiceErrorKind = "internal"
)

type runServiceError struct {
	kind runServiceErrorKind
	err  error
}

func (e runServiceError) Error() string {
	if e.err == nil {
		return string(e.kind)
	}
	return e.err.Error()
}

func (e runServiceError) Unwrap() error { return e.err }

func newRunService(server *Server) *RunService {
	return &RunService{
		jobs:            server.jobs,
		scheduler:       server.scheduling(),
		hclFile:         server.workspaceService().HCLFile,
		stateManager:    server.stateManager,
		requiredForTopo: requiredCapabilitiesForTopology,
		revisions:       server.apiStore,
		workspaces:      server.workspaceService(),
	}
}

func runError(kind runServiceErrorKind, err error) error {
	if err == nil {
		err = errors.New(string(kind))
	}
	return runServiceError{kind: kind, err: err}
}

func runServiceStatus(err error) int {
	var svcErr runServiceError
	if !errors.As(err, &svcErr) {
		return http.StatusInternalServerError
	}
	switch svcErr.kind {
	case runServiceBadRequest:
		return http.StatusBadRequest
	case runServiceConflict:
		return http.StatusConflict
	case runServiceNotFound:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func (s *RunService) StartApply(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	// Serialize the whole apply (HCL upsert + run creation) per-topology so
	// concurrent applies cannot race on the shared HCL file write.
	//
	// Coalescing below is in-memory: the per-topology mutex plus
	// startWithResult's deterministic OperationKey run id collapse concurrent
	// same-request applies into one run. That is correct for sysbox's
	// single-API-instance deployment. If multiple API instances ever share one
	// store, this path must additionally route through the durable
	// store.GetRunDispatch/CreateRunDispatch + RequestFingerprint mechanism
	// (as startIdempotentDestroy does) before coalescing.
	unlock := s.jobs.lockTopology(topology)
	defer unlock()

	var reqSnapshot string
	if req.Revision != "" && globalRevisionPattern.MatchString(req.Revision) {
		rev, err := s.revisions.GetGlobalRevision(ctx, req.Revision)
		if err != nil {
			if errors.Is(err, errGlobalRevisionNotFound) {
				return nil, runError(runServiceNotFound, err)
			}
			return nil, runError(runServiceInternal, err)
		}
		reqSnapshot, err = s.workspaces.MaterializeRevision(topology, req.Revision, rev.Files)
		if err != nil {
			return nil, runError(runServiceInternal, err)
		}
		if err := s.workspaces.UpsertProject(ctx, topology, rev.Files); err != nil {
			return nil, runError(runServiceInternal, err)
		}
	}
	operationKey := applyOperationKey(req.Revision, req.Inputs, req.AllowUnsafeState)
	opts := runStartOptions{
		Revision:     req.Revision,
		AgentID:      req.AgentID,
		UnsafeState:  req.AllowUnsafeState,
		Inputs:       req.Inputs,
		OperationKey: operationKey,
		SnapshotPath: reqSnapshot,
	}
	if req.DeadlineAt != nil {
		opts.DeadlineAt = *req.DeadlineAt
	}
	run, created := s.jobs.startWithResult(topology, "apply", opts)
	if !created {
		if run.Status.IsActive() {
			return run, nil // coalesced with an in-flight apply
		}
		// The deterministic id maps to a terminal run: replace it with a fresh
		// attempt that reuses the same id so subsequent applies coalesce onto
		// this in-flight run instead of the stale terminal record.
		run = newRun(topology, "apply", opts)
		s.jobs.forceStart(run)
	}
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

// applyOperationKey derives a deterministic operation key from the apply
// request. Inputs are canonicalised by sorting keys and every string field is
// length-prefixed, so equivalent maps hash to the same key regardless of map
// iteration order and distinct (revision, inputs, allow_unsafe_state) tuples
// cannot collide. The key itself is a sha256 hex string and contains no input
// plaintext. (Run.Inputs is transient: Jobs.persist strips it before the run is
// durably stored, so sensitive inputs never enter the state backend.)
func applyOperationKey(revision string, inputs map[string]string, allowUnsafe bool) string {
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "revision:%d:%s\n", len(revision), revision)
	fmt.Fprintf(&b, "allow_unsafe_state:%t\n", allowUnsafe)
	for _, k := range keys {
		v := inputs[k]
		fmt.Fprintf(&b, "input:%d:%s=%d:%s\n", len(k), k, len(v), v)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", sum[:])
}

func (s *RunService) StartRepair(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	snapshot, err := s.snapshotForRevision(ctx, topology, req.Revision)
	if err != nil {
		return nil, err
	}
	run := s.jobs.startWithOptions(topology, "repair", runStartOptions{
		Revision:     req.Revision,
		AgentID:      req.AgentID,
		UnsafeState:  req.AllowUnsafeState,
		SnapshotPath: snapshot,
	})
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *RunService) StartReset(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	snapshot, err := s.snapshotForRevision(ctx, topology, req.Revision)
	if err != nil {
		return nil, err
	}
	run := s.jobs.startWithOptions(topology, "reset", runStartOptions{
		Revision: req.Revision, AgentID: req.AgentID, Target: req.Target, UnsafeState: req.AllowUnsafeState,
		SnapshotPath: snapshot,
	})
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

// snapshotForRevision materializes a global content-addressed revision for
// lifecycle operations that accept a revision. Empty and legacy workspace
// revisions retain their existing compatibility behavior.
func (s *RunService) snapshotForRevision(ctx context.Context, topology, revision string) (string, error) {
	if revision == "" || !globalRevisionPattern.MatchString(revision) {
		return "", nil
	}
	rev, err := s.revisions.GetGlobalRevision(ctx, revision)
	if err != nil {
		return "", runError(runServiceNotFound, err)
	}
	path, err := s.workspaces.MaterializeRevision(topology, revision, rev.Files)
	if err != nil {
		return "", runError(runServiceInternal, err)
	}
	if err := s.workspaces.UpsertProject(ctx, topology, rev.Files); err != nil {
		return "", runError(runServiceInternal, err)
	}
	return path, nil
}

func (s *RunService) StartDestroy(ctx context.Context, topology string) (*controlplane.Run, error) {
	return s.StartDestroyWithOptions(ctx, topology, false)
}

func (s *RunService) StartDestroyWithOptions(ctx context.Context, topology string, allowUnsafe bool) (*controlplane.Run, error) {
	return s.startDestroy(ctx, topology, allowUnsafe, "")
}

func (s *RunService) StartDestroyWithOperationKey(ctx context.Context, topology string, allowUnsafe bool, operationKey string) (*controlplane.Run, error) {
	if err := validateOperationKey(operationKey); err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	return s.startDestroy(ctx, topology, allowUnsafe, operationKey)
}

func (s *RunService) startDestroy(ctx context.Context, topology string, allowUnsafe bool, operationKey string) (*controlplane.Run, error) {
	if operationKey != "" {
		return s.startIdempotentDestroy(ctx, topology, allowUnsafe, operationKey)
	}
	// Capture the current workspace before creating the run. Destroy follows
	// state lineage, but providers may still resolve workspace-relative data.
	// The snapshot prevents a later apply from changing the destroy input.
	snapshotID := fmt.Sprintf("destroy-%d", time.Now().UnixNano())
	snapshot, snapErr := s.workspaces.MaterializeWorkspaceSnapshot(topology, snapshotID)
	if snapErr == nil {
		// attached below after run creation
		_ = snapshot
	}
	run, created := s.jobs.startWithResult(topology, "destroy", runStartOptions{UnsafeState: allowUnsafe, OperationKey: operationKey})
	if !created {
		return run, nil
	}
	if snapErr == nil {
		run.SnapshotPath = snapshot
		s.jobs.persist(run)
	}
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	// The plain (non-idempotent) destroy is synchronous: it returns only after
	// the run reaches a terminal status, so exclusive address-space resources
	// (docker networks) are reclaimed before the caller moves on. A rebuild that
	// destroys then re-applies the same CIDR would otherwise race the still-
	// running destroy and fail with "Pool overlaps". The idempotent path below
	// stays asynchronous on purpose (its contract is dedup-enqueue, not wait).
	if err := s.waitForCompletion(ctx, run); err != nil {
		return nil, runError(runServiceInternal, err)
	}
	return run, nil
}

func (s *RunService) startIdempotentDestroy(ctx context.Context, topology string, allowUnsafe bool, operationKey string) (*controlplane.Run, error) {
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("topology=%s\noperation=destroy\nallow_unsafe_state=%t\n", topology, allowUnsafe))))
	run := newRun(topology, "destroy", runStartOptions{UnsafeState: allowUnsafe, OperationKey: operationKey})
	run.RequestFingerprint = fingerprint
	existing, found, err := s.jobs.store.GetRunDispatch(ctx, run.ID, fingerprint)
	if err != nil {
		if errors.Is(err, errIdempotencyConflict) {
			return nil, runError(runServiceConflict, err)
		}
		return nil, runError(runServiceInternal, err)
	}
	if found {
		s.jobs.remember(existing)
		return existing, nil
	}
	if snapshot, snapErr := s.workspaces.MaterializeWorkspaceSnapshot(topology, run.ID); snapErr == nil {
		run.SnapshotPath = snapshot
	}
	configPath := s.hclFile(topology)
	if run.SnapshotPath != "" {
		configPath = run.SnapshotPath
	}
	required, err := s.requiredForTopo(configPath)
	if err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	agent, err := s.scheduler.SelectAgentForTopology(ctx, topology, required, "")
	if err != nil {
		return nil, runError(runServiceConflict, err)
	}
	run.MarkAssigned(agent.ID, time.Now().UTC())
	command := controlplaneRunAssignedCommand(run)
	command.ID = "run-" + run.ID
	command.AgentID = agent.ID
	command.Status = controlplane.AgentCommandStatusQueued
	command.Protocol = controlplane.AgentProtocolVersion
	command.CreatedAt = time.Now().UTC()
	// Destroy has no inputs today, but if an apply path ever routes through
	// CreateRunDispatch, sensitive inputs must never be persisted here.
	run.Inputs = nil
	command.Run.Inputs = nil
	stored, created, err := s.jobs.store.CreateRunDispatch(ctx, RunDispatchRequest{Run: *run, Command: command, Fingerprint: fingerprint})
	if err != nil {
		if errors.Is(err, errIdempotencyConflict) {
			return nil, runError(runServiceConflict, err)
		}
		return nil, runError(runServiceInternal, err)
	}
	s.jobs.remember(stored)
	if created && s.scheduler.agents.registry != nil {
		_ = s.scheduler.agents.registry.PublishCommand(command.AgentID, command)
	}
	return stored, nil
}

// waitForCompletion blocks until run reaches a terminal status or the timeout
// elapses. The plain destroy path uses it so that exclusive address-space
// resources (docker networks) are reclaimed before the destroy call returns —
// otherwise a rebuild that re-applies the same CIDR races the still-running
// destroy and fails with "Pool overlaps". The SSE stream continues to report
// progress while waiting.
//
// Completion is signalled by the run's log broadcaster closing (Jobs.finish /
// Jobs.replace close it exactly when the run goes terminal), rather than by
// polling the durable store, so a destroy that runs to completion doesn't
// hammer the store backend.
func (s *RunService) waitForCompletion(ctx context.Context, run *controlplane.Run) error {
	b := s.jobs.logWriter(run.ID)
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	timer := time.NewTimer(destroySyncTimeout)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			// A log line arrived; keep waiting for close.
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("destroy %s did not complete within %s", run.ID, destroySyncTimeout)
		}
	}
}

func (s *RunService) Resume(ctx context.Context, runID string) (*controlplane.Run, *controlplane.Run, error) {
	parent, ok := s.jobs.get(runID)
	if !ok {
		return nil, nil, runError(runServiceNotFound, fmt.Errorf("run not found"))
	}
	if parent.Status == controlplane.RunRunning {
		return nil, parent, runError(runServiceConflict, fmt.Errorf("run %s is still running", runID))
	}
	if parent.Op != "apply" && parent.Op != "destroy" && parent.Op != "reset" {
		return nil, parent, runError(runServiceBadRequest, fmt.Errorf("run op %q cannot be resumed", parent.Op))
	}
	run := s.jobs.startChild(parent)
	if err := s.dispatchTopologyRun(ctx, run, run.Topology); err != nil {
		return nil, parent, err
	}
	return run, parent, nil
}

func (s *RunService) DispatchRun(ctx context.Context, run *controlplane.Run, required []string) error {
	return s.scheduler.DispatchRun(ctx, run, required)
}

func (s *RunService) dispatchTopologyRun(ctx context.Context, run *controlplane.Run, topology string) error {
	configPath := s.hclFile(topology)
	if run != nil && run.SnapshotPath != "" {
		configPath = run.SnapshotPath
	}
	required, err := s.requiredForTopo(configPath)
	if err != nil {
		s.jobs.finish(run, err)
		return runError(runServiceBadRequest, err)
	}
	if err := s.DispatchRun(ctx, run, required); err != nil {
		return runError(runServiceConflict, err)
	}
	return nil
}
