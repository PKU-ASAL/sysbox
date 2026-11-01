package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/workspace"
)

var errApplyInputsRequired = errors.New("apply inputs unavailable; resubmit the original request to this API")

func (s *RunService) startKeyedRevisionRun(ctx context.Context, topology, operation string, req RunStartRequest) (*controlplane.Run, error) {
	if err := validateOperationKey(req.OperationKey); err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	if !controlstore.GlobalRevisionPattern.MatchString(req.Revision) {
		return nil, runError(runServiceBadRequest, errors.New("keyed lifecycle operation requires an immutable global revision"))
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	var deadline time.Time
	if req.DeadlineAt != nil {
		deadline = req.DeadlineAt.UTC()
	}
	if len(req.Inputs) == 0 {
		req.Inputs = nil
	}
	fingerprint := controlplane.RunRequestFingerprint(operation, topology, req.Revision, req.AgentID, req.Target, req.AllowUnsafeState, req.Inputs, deadline)
	run := newRun(topology, operation, runStartOptions{Target: req.Target, Revision: req.Revision, AgentID: req.AgentID, UnsafeState: req.AllowUnsafeState, OperationKey: req.OperationKey, DeadlineAt: deadline})
	// Explicit request keys cannot alias restartable, content-derived run IDs.
	run.ID = fmt.Sprintf("request-%x", sha256.Sum256([]byte(topology+"\x00"+operation+"\x00"+req.OperationKey)))
	run.RequestFingerprint = fingerprint
	existing, found, err := s.jobs.store.GetRunDispatch(ctx, run.ID, fingerprint)
	if err != nil {
		return nil, dispatchError(err)
	}
	if found {
		return s.hydrateApply(existing, req.Inputs), nil
	}
	revision, err := s.revisions.GetGlobalRevision(ctx, req.Revision)
	if err != nil {
		if errors.Is(err, controlstore.ErrRevisionNotFound) {
			return nil, runError(runServiceNotFound, err)
		}
		return nil, runError(runServiceInternal, err)
	}
	// Admission pins an immutable tree; it never rewrites the workspace pointer.
	snapshot, err := s.workspaces.MaterializeRevision(topology, req.Revision, revision.Files)
	if err != nil {
		return nil, runError(runServiceInternal, err)
	}
	run.SnapshotPath = snapshot
	return s.dispatchKeyedRun(ctx, run, fingerprint, req.Inputs)
}

// The request mapping, canonical Run and command outbox are committed together.
func (s *RunService) dispatchKeyedRun(ctx context.Context, run *controlplane.Run, fingerprint string, inputs map[string]string) (*controlplane.Run, error) {
	required, err := s.requiredForTopo(run.SnapshotPath)
	if err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	agent, err := s.scheduler.SelectAgentForTopology(ctx, run.Topology, required, run.AgentID)
	if err != nil {
		return nil, runError(runServiceConflict, err)
	}
	run.MarkAssigned(agent.ID, time.Now().UTC())
	command, err := s.workspaces.RunAssignedCommand(ctx, run)
	if err != nil {
		return nil, runError(runServiceConflict, err)
	}
	stored, created, err := s.jobs.store.CreateRunDispatch(ctx, controlstore.RunDispatchRequest{Run: *run, Command: command, Fingerprint: fingerprint})
	if err != nil {
		return nil, dispatchError(err)
	}
	hydrated := s.hydrateApply(stored, inputs)
	if created && s.scheduler.agents.registry != nil {
		_ = s.scheduler.agents.registry.PublishCommand(command.AgentID, command)
	}
	return hydrated, nil
}

func dispatchError(err error) error {
	if errors.Is(err, controlstore.ErrIdempotencyConflict) || errors.Is(err, persistence.ErrConflict) {
		return runError(runServiceConflict, err)
	}
	return runError(runServiceInternal, err)
}

// Hydration uses the same incarnation-bound cache as every other run path.
// Only the already fingerprint-validated caller may supply these inputs.
func (s *RunService) hydrateApply(run *controlplane.Run, inputs map[string]string) *controlplane.Run {
	s.jobs.mu.Lock()
	snapshot := workspace.RunRecord(*run)
	if current := s.jobs.runs[run.ID]; current != nil && preferRun(current, &snapshot) {
		snapshot = workspace.RunRecord(*current)
	}
	snapshot.Inputs = nil
	if !snapshot.Status.IsTerminal() || (snapshot.Status == controlplane.RunFailed && snapshot.Recoverable) {
		snapshot.Inputs = cloneInputs(inputs)
		if snapshot.Inputs == nil {
			snapshot.Inputs = map[string]string{}
		}
	}
	s.jobs.runs[run.ID] = &snapshot
	result := snapshot
	result.Inputs = cloneInputs(snapshot.Inputs)
	s.jobs.mu.Unlock()
	s.jobs.logs.Ensure(run.ID, snapshot.Status.IsTerminal())
	return &result
}

func (j *Jobs) inputsRequired(run *controlplane.Run) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if run == nil {
		return false
	}
	return keyedInputsRequired(run, inputsForRecovery(j.runs[run.ID], run))
}

func keyedInputsRequired(run *controlplane.Run, inputs map[string]string) bool {
	return run != nil && (run.Status.IsActive() || (run.Status == controlplane.RunFailed && run.Recoverable)) && strings.HasPrefix(run.RequestFingerprint, "apply-request:") && inputs == nil
}

// Failed recoverable Runs may receive an explicit, fingerprint-checked
// resubmission. Ordinary claims still use inputsForRun and reject terminal Runs.
func inputsForRecovery(cached, current *controlplane.Run) map[string]string {
	if current == nil {
		return nil
	}
	if current.Status != controlplane.RunFailed || !current.Recoverable {
		return inputsForRun(cached, current)
	}
	if cached == nil || !controlplane.SameRunIncarnation(*cached, *current) {
		return nil
	}
	return cloneInputs(cached.Inputs)
}
