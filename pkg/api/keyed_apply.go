package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"time"
)

var errApplyInputsRequired = errors.New("keyed apply inputs must be resubmitted to this API before claiming")

func (s *RunService) startKeyedApply(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	if err := validateOperationKey(req.OperationKey); err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	if !globalRevisionPattern.MatchString(req.Revision) {
		return nil, runError(runServiceBadRequest, errors.New("keyed apply requires immutable global revision"))
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	raw, _ := json.Marshal(struct {
		Topology, Revision, Agent string
		Unsafe                    bool
		Inputs                    map[string]string
		Deadline                  *time.Time
	}{topology, req.Revision, req.AgentID, req.AllowUnsafeState, req.Inputs, req.DeadlineAt})
	fingerprint := fmtHash(raw)
	opts := runStartOptions{Revision: req.Revision, AgentID: req.AgentID, UnsafeState: req.AllowUnsafeState, OperationKey: req.OperationKey}
	if req.DeadlineAt != nil {
		opts.DeadlineAt = req.DeadlineAt.UTC()
	}
	run := newRun(topology, "apply", opts)
	run.RequestFingerprint = fingerprint
	existing, found, err := s.jobs.store.GetRunDispatch(ctx, run.ID, fingerprint)
	if err != nil {
		return nil, dispatchError(err)
	}
	if found {
		return s.hydrateApply(existing, req.Inputs), nil
	}
	if err := s.rejectActive(ctx, topology, run.ID); err != nil {
		replay, found, lookupErr := s.jobs.store.GetRunDispatch(ctx, run.ID, fingerprint)
		if lookupErr != nil {
			return nil, dispatchError(lookupErr)
		}
		if found {
			return s.hydrateApply(replay, req.Inputs), nil
		}
		return nil, err
	}
	snapshot, err := s.snapshotForRevision(ctx, topology, req.Revision)
	if err != nil {
		return nil, err
	}
	run.SnapshotPath = snapshot
	required, err := s.requiredForTopo(snapshot)
	if err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	agent, err := s.scheduler.SelectAgentForTopology(ctx, topology, required, req.AgentID)
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
	stored, created, err := s.jobs.store.CreateRunDispatch(ctx, RunDispatchRequest{Run: *run, Command: command, Fingerprint: fingerprint})
	if err != nil {
		return nil, dispatchError(err)
	}
	s.hydrateApply(stored, req.Inputs)
	if created && s.scheduler.agents.registry != nil {
		_ = s.scheduler.agents.registry.PublishCommand(command.AgentID, command)
	}
	return stored, nil
}
func fmtHash(raw []byte) string { sum := sha256.Sum256(raw); return fmt.Sprintf("%x", sum[:]) }
func dispatchError(err error) error {
	if errors.Is(err, errIdempotencyConflict) || errors.Is(err, errActiveOperation) {
		return runError(runServiceConflict, err)
	}
	return runError(runServiceInternal, err)
}
func (s *RunService) hydrateApply(run *controlplane.Run, inputs map[string]string) *controlplane.Run {
	s.jobs.mu.Lock()
	snapshot := runRecord(*run)
	if current := s.jobs.runs[run.ID]; current != nil && preferRun(current, &snapshot) {
		snapshot = runRecord(*current)
	}
	s.jobs.runs[run.ID] = &snapshot
	if snapshot.Status.IsTerminal() {
		delete(s.jobs.applyInputs, run.ID)
	} else {
		copy := cloneInputs(inputs)
		if copy == nil {
			copy = map[string]string{}
		}
		s.jobs.applyInputs[run.ID] = copy
	}
	s.jobs.mu.Unlock()
	s.jobs.logs.Ensure(run.ID, snapshot.Status.IsTerminal())
	return &snapshot
}

func (s *RunService) rejectActive(ctx context.Context, topology string, except ...string) error {
	runs, err := s.jobs.store.LoadRuns(ctx)
	if err != nil {
		return runError(runServiceInternal, err)
	}
	for _, r := range latestRunsByID(runs) {
		if len(except) > 0 && r.ID == except[0] {
			continue
		}
		if r.Topology == topology && r.Status.IsActive() {
			return runError(runServiceConflict, errors.New("topology has an active operation"))
		}
	}
	return nil
}
func (j *Jobs) inputsRequired(run *controlplane.Run) bool {
	if run.Op != "apply" || run.RequestFingerprint == "" || run.Status.IsTerminal() {
		return false
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	_, ok := j.applyInputs[run.ID]
	return !ok
}

// Legacy requests may keep their coalescing semantics, but must not bypass an
// already admitted durable operation on the same workspace.
func (s *RunService) rejectActiveKeyed(ctx context.Context, topology string) error {
	runs, err := s.jobs.store.LoadRuns(ctx)
	if err != nil {
		return runError(runServiceInternal, err)
	}
	for _, run := range latestRunsByID(runs) {
		if run.Topology == topology && run.RequestFingerprint != "" && run.Status.IsActive() {
			return runError(runServiceConflict, errActiveOperation)
		}
	}
	return nil
}
