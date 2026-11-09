package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/workspace"
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
	revisions       controlstore.GlobalRevisionStore
	workspaces      *workspace.Catalog
	checkpoints     runtime.CheckpointLoader
}

type RunStartRequest struct {
	OperationKey     string
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
		checkpoints:     server.apiStore,
	}
}

func runError(kind runServiceErrorKind, err error) error {
	if err == nil {
		err = errors.New(string(kind))
	}
	return runServiceError{kind: kind, err: err}
}

func runServiceStatus(err error) int {
	if errors.Is(err, controlstore.ErrActiveOperation) {
		return http.StatusConflict
	}
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
	if req.Revision != "" && !controlstore.GlobalRevisionPattern.MatchString(req.Revision) {
		return nil, runError(runServiceBadRequest, errors.New("revision must be an immutable global digest; republish old workspace revisions"))
	}
	if req.Target != "" && req.OperationKey == "" {
		return nil, runError(runServiceBadRequest, errors.New("targeted apply requires Idempotency-Key"))
	}
	if req.OperationKey != "" {
		return s.startKeyedRevisionRun(ctx, topology, "apply", req, false)
	}
	// Legacy unkeyed apply: the same durable admission as a keyed apply, with a
	// content-derived operation key, plus the mutable workspace pointer that
	// old readers still expect. It is no longer an in-memory-only coalescing
	// path, so a restart cannot dispatch a second run for the same request.
	req.OperationKey = applyOperationKey(req.Revision, req.Inputs, req.AllowUnsafeState)
	return s.startKeyedRevisionRun(ctx, topology, "apply", req, true)
}

// applyOperationKey derives a deterministic operation key from the apply
// request. Inputs are canonicalised by sorting keys and every string field is
// length-prefixed, so equivalent maps hash to the same key regardless of map
// iteration order and distinct (revision, inputs, allow_unsafe_state) tuples
// cannot collide. The key itself is a sha256 hex string and contains no input
// plaintext. (Run.Inputs is transient: Jobs.persist strips it before the run is
// durably stored, so sensitive inputs never enter the state backend.)
func applyOperationKey(revision string, inputs map[string]string, allowUnsafe bool) string {
	return controlplane.ApplyOperationKey(revision, inputs, allowUnsafe)
}

func (s *RunService) StartRepair(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	if err := s.rejectActiveTopology(ctx, topology, ""); err != nil {
		return nil, err
	}
	snapshot, err := s.snapshotForRevision(ctx, topology, req.Revision)
	if err != nil {
		return nil, err
	}
	run, _, err := s.jobs.startWithResult(topology, "repair", runStartOptions{
		Revision:     req.Revision,
		AgentID:      req.AgentID,
		UnsafeState:  req.AllowUnsafeState,
		SnapshotPath: snapshot,
	})
	if err != nil {
		return nil, runError(runServiceInternal, err)
	}
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *RunService) StartReset(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	if len(req.Inputs) > 0 && (req.OperationKey == "" || req.Revision == "") {
		return nil, runError(runServiceBadRequest, errors.New("reset inputs require an idempotency key and immutable revision"))
	}
	if req.OperationKey != "" {
		return s.startKeyedRevisionRun(ctx, topology, "reset", req, false)
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	if err := s.rejectActiveTopology(ctx, topology, ""); err != nil {
		return nil, err
	}
	snapshot, err := s.snapshotForRevision(ctx, topology, req.Revision)
	if err != nil {
		return nil, err
	}
	run, _, err := s.jobs.startWithResult(topology, "reset", runStartOptions{
		Revision: req.Revision, AgentID: req.AgentID, Target: req.Target, UnsafeState: req.AllowUnsafeState,
		SnapshotPath: snapshot,
	})
	if err != nil {
		return nil, runError(runServiceInternal, err)
	}
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

// snapshotForRevision materializes a global content-addressed revision for
// lifecycle operations that accept a revision. Empty selects the current workspace.
func (s *RunService) snapshotForRevision(ctx context.Context, topology, revision string) (string, error) {
	if revision == "" {
		path, err := s.admittedApplySnapshot(ctx, topology)
		if err != nil || path != "" {
			return path, err
		}
		path, err = s.workspaces.MaterializeWorkspaceSnapshot(topology, fmt.Sprintf("lifecycle-%d", time.Now().UnixNano()))
		if err != nil {
			return "", runError(runServiceBadRequest, err)
		}
		return path, nil
	}
	if !controlstore.GlobalRevisionPattern.MatchString(revision) {
		return "", runError(runServiceBadRequest, errors.New("revision must be an immutable global digest; republish old workspace revisions"))
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

func (s *RunService) rejectActiveTopology(ctx context.Context, topology, exceptID string) error {
	runs, err := s.jobs.store.LoadRuns(ctx)
	if err != nil {
		return runError(runServiceInternal, err)
	}
	if err := controlstore.ValidateTopologyAdmission(runs, controlplane.Run{ID: exceptID, Topology: topology, Status: controlplane.RunQueued}); err != nil {
		return runError(runServiceConflict, err)
	}
	return nil
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
	unlock := s.jobs.lockTopology(topology)
	run, err := s.admitDestroy(ctx, topology, allowUnsafe, operationKey)
	unlock()
	if err != nil {
		return nil, err
	}
	if operationKey == "" {
		if err := s.waitForCompletion(ctx, run); err != nil {
			return nil, runError(runServiceInternal, err)
		}
	}
	return run, nil
}

func (s *RunService) admitDestroy(ctx context.Context, topology string, allowUnsafe bool, operationKey string) (*controlplane.Run, error) {
	if operationKey != "" {
		return s.startIdempotentDestroy(ctx, topology, allowUnsafe, operationKey)
	}
	// Capture the current workspace before creating the run. Destroy follows
	// state lineage, but providers may still resolve workspace-relative data.
	// The snapshot prevents a later apply from changing the destroy input.
	snapshotID := fmt.Sprintf("destroy-%d", time.Now().UnixNano())
	snapshot, err := s.admittedApplySnapshot(ctx, topology)
	if err != nil {
		return nil, err
	}
	var snapErr error
	if snapshot == "" {
		snapshot, snapErr = s.workspaces.MaterializeWorkspaceSnapshot(topology, snapshotID)
	}
	run, created, err := s.jobs.startWithResult(topology, "destroy", runStartOptions{UnsafeState: allowUnsafe, OperationKey: operationKey})
	if err != nil {
		return nil, runError(runServiceInternal, err)
	}
	if !created {
		return run, nil
	}
	if snapErr == nil {
		run.SnapshotPath = snapshot
	}
	if err := s.pinDestroyRevision(ctx, run); err != nil {
		return nil, runError(runServiceInternal, err)
	}
	if err := s.jobs.persist(run); err != nil {
		return nil, runError(runServiceInternal, err)
	}
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *RunService) startIdempotentDestroy(ctx context.Context, topology string, allowUnsafe bool, operationKey string) (*controlplane.Run, error) {
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("topology=%s\noperation=destroy\nallow_unsafe_state=%t\n", topology, allowUnsafe))))
	run := newRun(topology, "destroy", runStartOptions{UnsafeState: allowUnsafe, OperationKey: operationKey})
	run.RequestFingerprint = fingerprint
	existing, found, err := s.jobs.store.GetRunDispatch(ctx, run.ID, fingerprint)
	if err != nil {
		if errors.Is(err, controlstore.ErrIdempotencyConflict) {
			return nil, runError(runServiceConflict, err)
		}
		return nil, runError(runServiceInternal, err)
	}
	if found {
		s.jobs.remember(existing)
		return existing, nil
	}
	snapshot, err := s.admittedApplySnapshot(ctx, topology)
	if err != nil {
		return nil, err
	}
	if snapshot == "" {
		// Destroy can still use state when workspace metadata has been removed.
		snapshot, _ = s.workspaces.MaterializeWorkspaceSnapshot(topology, run.ID)
	}
	run.SnapshotPath = snapshot
	if err := s.pinDestroyRevision(ctx, run); err != nil {
		return nil, runError(runServiceInternal, err)
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
	command, err := s.workspaces.RunAssignedCommand(ctx, run)
	if err != nil {
		return nil, runError(runServiceInternal, err)
	}
	// Destroy has no inputs today, but if an apply path ever routes through
	// CreateRunDispatch, sensitive inputs must never be persisted here.
	run.Inputs = nil
	command.Run.Inputs = nil
	stored, created, err := s.jobs.store.CreateRunDispatch(ctx, controlstore.RunDispatchRequest{Run: *run, Command: command, Fingerprint: fingerprint})
	if err != nil {
		if errors.Is(err, controlstore.ErrIdempotencyConflict) {
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

// A keyed apply does not publish a mutable workspace pointer. Subsequent
// lifecycle operations use its admitted immutable intent while placement still
// anchors that topology. Removed metadata never revives a historical snapshot.
func (s *RunService) admittedApplySnapshot(ctx context.Context, topology string) (string, error) {
	placement, err := s.workspaces.LoadPlacement(topology)
	if err != nil {
		return "", runError(runServiceInternal, err)
	}
	if placement == nil {
		return "", nil
	}
	runs, err := s.jobs.store.LoadRuns(ctx)
	if err != nil {
		return "", runError(runServiceInternal, err)
	}
	var latest *controlplane.Run
	for _, run := range controlstore.LatestRunsByID(runs) {
		if run.Topology != topology || run.Op != "apply" || run.RequestFingerprint == "" {
			continue
		}
		if latest == nil || run.QueuedAt.After(latest.QueuedAt) {
			copy := run
			latest = &copy
		}
	}
	if latest == nil {
		return "", nil
	}
	if !strings.HasPrefix(latest.RequestFingerprint, "apply-request:") {
		return "", nil
	}
	if latest.AgentID != placement.AgentID || latest.SnapshotPath == "" {
		return "", runError(runServiceConflict, errors.New("admitted apply has no matching placement and snapshot"))
	}
	if _, err := os.Stat(latest.SnapshotPath); err != nil {
		return "", runError(runServiceConflict, fmt.Errorf("admitted snapshot unavailable; restore its immutable revision: %w", err))
	}
	return latest.SnapshotPath, nil
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
	return s.ResumeWithKey(ctx, runID, "")
}

func (s *RunService) ResumeWithKey(ctx context.Context, runID, key string) (*controlplane.Run, *controlplane.Run, error) {
	return s.ResumeWithInputs(ctx, runID, key, nil)
}

func (s *RunService) ResumeWithInputs(ctx context.Context, runID, key string, inputs map[string]string) (*controlplane.Run, *controlplane.Run, error) {
	if key != "" {
		if err := validateOperationKey(key); err != nil {
			return nil, nil, runError(runServiceBadRequest, err)
		}
	}
	parent, ok := s.jobs.get(runID)
	if !ok {
		return nil, nil, runError(runServiceNotFound, fmt.Errorf("run not found"))
	}
	boundReset := parent.Op == "reset" && controlplane.HasBoundRunInputs(*parent)
	if len(inputs) > 0 && !boundReset {
		return nil, parent, runError(runServiceBadRequest, errors.New("resume inputs require an input-bound reset"))
	}
	if boundReset {
		if key == "" {
			return nil, parent, runError(runServiceBadRequest, errors.New("input-bound reset resume requires an idempotency key"))
		}
		if err := controlplane.ValidateRunInputs(*parent, inputs); err != nil {
			return nil, parent, runError(runServiceConflict, err)
		}
	}
	unlock := s.jobs.lockTopology(parent.Topology)
	defer unlock()
	var keyed *controlplane.Run
	if key != "" {
		target := parent.Target
		if parent.Op == "reset" {
			target = ""
		}
		keyed = newRun(parent.Topology, parent.Op, runStartOptions{ParentID: parent.ID, SnapshotPath: parent.SnapshotPath, Revision: parent.Revision, Target: target, AgentID: parent.AgentID, UnsafeState: parent.UnsafeState, OperationKey: key})
		keyed.ID = fmt.Sprintf("continuation-%x", sha256.Sum256([]byte(parent.ID+"\x00"+key)))
		keyed.RequestFingerprint = fmt.Sprintf("continuation-request:%x", sha256.Sum256([]byte(parent.ID+"\x00"+parent.QueuedAt.UTC().Format(time.RFC3339Nano)+"\x00"+parent.Revision)))
		if boundReset {
			keyed.RequestFingerprint = controlplane.ResetContinuationFingerprint(*parent, *keyed, inputs)
		}
		existing, found, err := s.jobs.store.GetRunDispatch(ctx, keyed.ID, keyed.RequestFingerprint)
		if err != nil {
			return nil, parent, dispatchError(err)
		}
		if found {
			if boundReset {
				return s.hydrateApply(existing, inputs), parent, nil
			}
			s.jobs.remember(existing)
			return existing, parent, nil
		}
	}
	if parent.Status == controlplane.RunRunning {
		return nil, parent, runError(runServiceConflict, fmt.Errorf("run %s is still running", runID))
	}
	if err := s.rejectActiveTopology(ctx, parent.Topology, ""); err != nil {
		return nil, parent, err
	}
	if parent.Op != "apply" && parent.Op != "destroy" && parent.Op != "reset" {
		return nil, parent, runError(runServiceBadRequest, fmt.Errorf("run op %q cannot be resumed", parent.Op))
	}
	if parent.Op == "reset" {
		if !parent.Status.IsTerminal() {
			return nil, parent, runError(runServiceConflict, fmt.Errorf("parent reset is not terminal"))
		}
		checkpoint, err := s.checkpoints.LoadCheckpoint(ctx, parent.Topology, parent.ID)
		if err != nil || checkpoint == nil || checkpoint.RunID != parent.ID || checkpoint.Operation != "reset" || checkpoint.PlanSHA256 == "" {
			if err != nil {
				err = fmt.Errorf("reset continuation requires an authenticated complete parent checkpoint: %w", err)
			} else {
				err = fmt.Errorf("reset continuation requires an authenticated complete parent checkpoint")
			}
			return nil, parent, runError(runServiceConflict, err)
		}
		if checkpoint.Topology != parent.Topology || len(checkpoint.Plan) == 0 {
			return nil, parent, runError(runServiceConflict, fmt.Errorf("reset checkpoint scope mismatch"))
		}
		if _, err := runtime.ResumeResetPlan(checkpoint, &runtime.Plan{Actions: checkpoint.Plan}); err != nil {
			return nil, parent, runError(runServiceConflict, err)
		}
	}
	if keyed != nil {
		run, err := s.dispatchKeyedRun(ctx, keyed, keyed.RequestFingerprint, inputs)
		return run, parent, err
	}
	run, err := s.jobs.startChild(parent)
	if err != nil {
		return nil, parent, runError(runServiceInternal, err)
	}
	if err := s.dispatchTopologyRun(ctx, run, run.Topology); err != nil {
		return nil, parent, err
	}
	return run, parent, nil
}

func (s *RunService) DispatchRun(ctx context.Context, run *controlplane.Run, required []string) error {
	return s.scheduler.DispatchRun(ctx, run, required)
}

func (s *RunService) dispatchTopologyRun(ctx context.Context, run *controlplane.Run, topology string) error {
	// Destroy is driven by persisted state. If its workspace HCL was already
	// removed, requiring capability discovery from that file prevents cleanup
	// of the very stale resources destroy is meant to release. The scheduler can
	// select the topology's existing placement without HCL-derived capabilities.
	if run != nil && run.Op == "destroy" {
		if err := s.DispatchRun(ctx, run, nil); err != nil {
			return runError(runServiceConflict, err)
		}
		return nil
	}
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
