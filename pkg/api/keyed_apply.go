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

// startKeyedRevisionRun is the single durable admission path for apply and
// reset. Callers that provide only a workspace (the legacy unkeyed CLI flow)
// set publishPointer: they additionally publish the mutable workspace pointer
// for old readers, but the canonical facts — immutable revision snapshot,
// fingerprint and run dispatch — are identical to the keyed path.
func (s *RunService) startKeyedRevisionRun(ctx context.Context, topology, operation string, req RunStartRequest, publishPointer bool) (*controlplane.Run, error) {
	if err := validateOperationKey(req.OperationKey); err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	if req.Revision != "" && !controlstore.GlobalRevisionPattern.MatchString(req.Revision) {
		return nil, runError(runServiceBadRequest, errors.New("lifecycle operation requires an immutable global revision"))
	}
	if req.Revision == "" && !publishPointer {
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
		hydrated := s.hydrateApply(existing, req.Inputs)
		if !publishPointer || hydrated.Status.IsActive() {
			return hydrated, nil
		}
		// Legacy interactive re-apply: a terminal record for the same request
		// is restarted in place so `sysbox apply` after a failed or completed
		// run does work again. Keyed callers never take this branch; for them a
		// terminal replay is the answer.
		snapshot, err := s.applySnapshot(ctx, topology, req, publishPointer)
		if err != nil {
			return nil, err
		}
		retry := newRun(topology, operation, runStartOptions{
			Revision: req.Revision, AgentID: req.AgentID, UnsafeState: req.AllowUnsafeState,
			Inputs: req.Inputs, OperationKey: req.OperationKey, SnapshotPath: snapshot, DeadlineAt: deadline,
		})
		retry.ID = hydrated.ID
		retry.RequestFingerprint = hydrated.RequestFingerprint
		if err := s.jobs.forceStart(retry); err != nil {
			return nil, dispatchError(err)
		}
		if err := s.dispatchTopologyRun(ctx, retry, topology); err != nil {
			return nil, err
		}
		return retry, nil
	}
	// Reject a concurrent apply on the same topology before any workspace
	// effect: a rejected request must not rewrite the HCL pointer.
	if operation == "apply" {
		if err := s.rejectActiveTopology(ctx, topology, run.ID); err != nil {
			return nil, err
		}
	}
	snapshot, err := s.applySnapshot(ctx, topology, req, publishPointer)
	if err != nil {
		return nil, err
	}
	run.SnapshotPath = snapshot
	return s.dispatchKeyedRun(ctx, run, fingerprint, req.Inputs)
}

// applySnapshot pins the immutable tree every apply runs against. The mutable
// workspace pointer is a compatibility cache published only for legacy
// unkeyed callers; keyed callers never rewrite it.
func (s *RunService) applySnapshot(ctx context.Context, topology string, req RunStartRequest, publishPointer bool) (string, error) {
	if req.Revision == "" {
		return s.snapshotForRevision(ctx, topology, "")
	}
	revision, err := s.revisions.GetGlobalRevision(ctx, req.Revision)
	if err != nil {
		if errors.Is(err, controlstore.ErrRevisionNotFound) {
			return "", runError(runServiceNotFound, err)
		}
		return "", runError(runServiceInternal, err)
	}
	snapshot, err := s.workspaces.MaterializeRevision(topology, req.Revision, revision.Files)
	if err != nil {
		return "", runError(runServiceInternal, err)
	}
	if publishPointer {
		if err := s.workspaces.UpsertProject(ctx, topology, revision.Files); err != nil {
			return "", runError(runServiceInternal, err)
		}
	}
	return snapshot, nil
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
