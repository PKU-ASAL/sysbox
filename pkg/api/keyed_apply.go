package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

var errActiveOperation = fmt.Errorf("%w: topology has an active operation", persistence.ErrConflict)
var errApplyInputsRequired = errors.New("apply inputs unavailable; resubmit the original request to this API")

func (s *RunService) startKeyedApply(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	if err := validateOperationKey(req.OperationKey); err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	if !globalRevisionPattern.MatchString(req.Revision) {
		return nil, runError(runServiceBadRequest, errors.New("keyed apply requires an immutable global revision"))
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
	raw, _ := json.Marshal(struct {
		Topology, Revision, Agent string
		Unsafe                    bool
		Inputs                    map[string]string
		Deadline                  time.Time
	}{topology, req.Revision, req.AgentID, req.AllowUnsafeState, req.Inputs, deadline})
	fingerprint := fmt.Sprintf("apply-request:%x", sha256.Sum256(raw))
	run := newRun(topology, "apply", runStartOptions{Revision: req.Revision, AgentID: req.AgentID, UnsafeState: req.AllowUnsafeState, OperationKey: req.OperationKey, DeadlineAt: deadline})
	// Explicit request keys cannot alias restartable, content-derived run IDs.
	run.ID = fmt.Sprintf("request-%x", sha256.Sum256([]byte(topology+"\x00apply\x00"+req.OperationKey)))
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
		if errors.Is(err, errGlobalRevisionNotFound) {
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
	command.ID = runCommandID(*run)
	command.AgentID = agent.ID
	command.Status = controlplane.AgentCommandStatusQueued
	command.Protocol = controlplane.AgentProtocolVersion
	command.CreatedAt = time.Now().UTC()
	stored, created, err := s.jobs.store.CreateRunDispatch(ctx, RunDispatchRequest{Run: *run, Command: command, Fingerprint: fingerprint})
	if err != nil {
		return nil, dispatchError(err)
	}
	hydrated := s.hydrateApply(stored, req.Inputs)
	if created && s.scheduler.agents.registry != nil {
		_ = s.scheduler.agents.registry.PublishCommand(command.AgentID, command)
	}
	return hydrated, nil
}

func dispatchError(err error) error {
	if errors.Is(err, errIdempotencyConflict) || errors.Is(err, persistence.ErrConflict) {
		return runError(runServiceConflict, err)
	}
	return runError(runServiceInternal, err)
}

// Hydration uses the same incarnation-bound cache as every other run path.
// Only the already fingerprint-validated caller may supply these inputs.
func (s *RunService) hydrateApply(run *controlplane.Run, inputs map[string]string) *controlplane.Run {
	s.jobs.mu.Lock()
	snapshot := runRecord(*run)
	if current := s.jobs.runs[run.ID]; current != nil && preferRun(current, &snapshot) {
		snapshot = runRecord(*current)
	}
	snapshot.Inputs = nil
	if !snapshot.Status.IsTerminal() {
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
	return keyedInputsRequired(run, inputsForRun(j.runs[run.ID], run))
}

func keyedInputsRequired(run *controlplane.Run, inputs map[string]string) bool {
	return run != nil && run.Status.IsActive() && strings.HasPrefix(run.RequestFingerprint, "apply-request:") && inputs == nil
}

func validateTopologyAdmission(runs []controlplane.Run, next controlplane.Run) error {
	if !next.Status.IsActive() {
		return nil
	}
	for _, run := range latestRunsByID(runs) {
		if run.Topology == next.Topology && run.ID != next.ID && run.Status.IsActive() {
			return errActiveOperation
		}
	}
	return nil
}
