package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"net/http"
	"slices"
	"time"
)

// recoveryAuthorization is re-evaluated immediately before Agent work. It does
// not transfer execution ownership or mark a running Run done.
func (s *Server) recoveryAuthorization(ctx context.Context, run controlplane.Run, mode string) (controlplane.RunRecoveryAuthorization, error) {
	result := controlplane.RunRecoveryAuthorization{Run: run, Mode: mode}
	if mode == controlplane.RunRecoveryObserve {
		return result, nil
	}
	if mode != controlplane.RunRecoveryRecover {
		return result, persistence.ErrInvalidRecord
	}
	if run.Status != controlplane.RunFailed || !run.Recoverable || run.LeaseUntil.After(time.Now().UTC()) {
		return result, persistence.ErrConflict
	}
	runs, err := s.apiStore.LoadRuns(ctx)
	if err != nil {
		return result, err
	}
	for _, other := range latestRunsByID(runs) {
		if other.Topology == run.Topology && other.Status.IsActive() {
			return result, persistence.ErrConflict
		}
	}
	if !globalRevisionPattern.MatchString(run.Revision) {
		return result, fmt.Errorf("%w: recovery requires an immutable revision", persistence.ErrInvalidRecord)
	}
	// Non-default inputs are intentionally not persisted. Do not silently
	// reconstruct them as defaults while authorizing recovery.
	if run.Op == "apply" && run.OperationKey != "" && run.OperationKey != applyOperationKey(run.Revision, nil, run.UnsafeState) {
		return result, fmt.Errorf("%w: recovery inputs must be resubmitted", persistence.ErrConflict)
	}
	revision, err := s.apiStore.GetGlobalRevision(ctx, run.Revision)
	if err != nil {
		return result, err
	}
	if controlplane.ComputeRevisionDigest(revision.Files) != run.Revision {
		return result, persistence.ErrCorruptRecord
	}
	result.Revision = revision
	return result, nil
}
func (s *Server) handleRequestRunRecovery(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	id := r.PathValue("id")
	if err := validatePathSegment(id, "run"); err != nil {
		writeError(w, 400, err)
		return
	}
	run, err := s.apiStore.GetRun(r.Context(), id)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	mode := r.PathValue("mode")
	if mode != "observe" && mode != "recover" {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	// A controller restart does not cancel a live worker. Only the existing
	// atomic lease-expiry transition can release an interrupted running Run.
	if mode == controlplane.RunRecoveryRecover && run.Status == controlplane.RunRunning {
		expired, _, expireErr := s.apiStore.ExpireRunLease(r.Context(), run.ID, time.Now().UTC())
		if expireErr != nil {
			writeError(w, agentRepositoryErrorStatus(expireErr), expireErr)
			return
		}
		run = expired
	}
	if run.AgentID == "" {
		writeError(w, 409, fmt.Errorf("run has no owning agent"))
		return
	}
	agent, err := s.apiStore.GetAgent(r.Context(), run.AgentID)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if agent.IsBlocked() || !slices.Contains(agent.Capabilities, controlplane.RunRecoveryCapability) {
		writeError(w, 409, fmt.Errorf("owning agent does not support run recovery"))
		return
	}
	if _, err = s.recoveryAuthorization(r.Context(), *run, mode); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	snapshot := persistence.DurableRun(*run)
	cmd := controlplane.AgentCommand{ID: uuid.NewString(), AgentID: run.AgentID, Type: controlplane.RunRecoveryCommand, RecoveryMode: mode, Run: &snapshot, Protocol: controlplane.AgentProtocolVersion, Status: controlplane.AgentCommandStatusQueued, CreatedAt: time.Now().UTC()}
	if err := s.apiStore.SaveAgentCommand(r.Context(), cmd); err != nil {
		writeError(w, 500, err)
		return
	}
	if s.agents != nil {
		_ = s.agents.PublishCommand(cmd.AgentID, cmd)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"command_id": cmd.ID, "run_id": run.ID, "mode": mode})
}
func (s *Server) handleAuthorizeRunRecovery(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if err := s.verifyAgentRequest(r, agent); err != nil {
		writeError(w, 401, err)
		return
	}
	owner, err := s.apiStore.GetAgent(r.Context(), agent)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if owner.IsBlocked() || !slices.Contains(owner.Capabilities, controlplane.RunRecoveryCapability) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	var proof controlplane.AgentCommandEvent
	if err := json.NewDecoder(r.Body).Decode(&proof); err != nil {
		writeError(w, 400, fmt.Errorf("decode recovery proof"))
		return
	}
	cmd, err := s.agentService().FindCommand(r.Context(), agent, r.PathValue("command"))
	if err != nil {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if cmd.IsTerminal() || cmd.Type != controlplane.RunRecoveryCommand || cmd.Run == nil || proof.LeaseOwner == "" || proof.LeaseOwner != cmd.LeaseOwner || proof.Attempt < 1 || proof.Attempt != cmd.Attempt || !cmd.LeaseUntil.After(time.Now().UTC()) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	run, err := s.apiStore.GetRun(r.Context(), cmd.Run.ID)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if !persistence.SameRunIncarnation(*run, *cmd.Run) || run.Attempt != cmd.Run.Attempt || run.LeaseOwner != cmd.Run.LeaseOwner {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	auth, err := s.recoveryAuthorization(r.Context(), *run, cmd.RecoveryMode)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	auth.Run = persistence.DurableRun(auth.Run)
	writeJSON(w, 200, auth)
}
func (s *Server) handleGetRunRecovery(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	run, err := s.apiStore.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	report, err := s.apiStore.GetRunRecoveryReport(r.Context(), run.AgentID, r.PathValue("command"))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if report.RunID != run.ID || !report.QueuedAt.Equal(run.QueuedAt) || report.Revision != run.Revision || report.RunAttempt != run.Attempt || report.RunLeaseOwner != run.LeaseOwner {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	writeJSON(w, 200, report)
}
func (b *ExecutionBridge) PinnedRevisionWorkspace(ctx context.Context, run controlplane.Run, revision *controlplane.GlobalRevision) (string, error) {
	if revision == nil || revision.Revision != run.Revision || controlplane.ComputeRevisionDigest(revision.Files) != run.Revision {
		return "", persistence.ErrConflict
	}
	return b.server.workspaceService().MaterializeRevision(run.Topology, run.Revision, revision.Files)
}
