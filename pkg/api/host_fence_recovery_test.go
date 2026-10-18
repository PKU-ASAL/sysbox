package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func fenceRecoveryRequest(s *Server, method, path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Sysbox-Roles", "admin")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}
func checkFenceBeforeReceipt(t *testing.T, s *Server, fence persistence.HostFenceRecord) {
	t.Helper()
	path := "/v1/topologies/lab/host-fences/" + fence.Request.ID
	denied := httptest.NewRecorder()
	s.ServeHTTP(denied, httptest.NewRequest("GET", path+"/recovery-plan", nil))
	require.Equal(t, 403, denied.Code)
	got := fenceRecoveryRequest(s, "GET", path+"/recovery-plan", "")
	require.Equal(t, 200, got.Code, got.Body.String())
	require.Contains(t, got.Body.String(), "fence_receipt_missing")
	require.Equal(t, 409, fenceRecoveryRequest(s, "POST", path+"/observe", "first").Code)
}
func checkFenceObservationContract(t *testing.T, s *Server, store apiStore, fence persistence.HostFenceRecord) {
	t.Helper()
	ctx := context.Background()
	path := "/v1/topologies/lab/host-fences/" + fence.Request.ID
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	before, actions, err := repo.LoadStateAndActions(ctx, "lab")
	require.NoError(t, err)
	run, err := store.GetRun(ctx, fence.Request.RunID)
	require.NoError(t, err)
	require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", Status: "online", Capabilities: []string{controlplane.RunRecoveryCapability}}))
	got := fenceRecoveryRequest(s, "GET", path+"/recovery-plan", "")
	require.Equal(t, 200, got.Code, got.Body.String())
	var plan hostFenceRecoveryPlan
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &plan))
	require.True(t, plan.JournalMatches)
	require.Equal(t, "verified", plan.HostProcessExclusion)
	require.False(t, plan.ReleaseAllowed)
	require.False(t, plan.ReplayAllowed)
	require.Len(t, plan.Actions, 1)
	require.Contains(t, plan.Actions[0].Blockers, "missing_original_operation_anchor")
	require.Equal(t, 400, fenceRecoveryRequest(s, "POST", path+"/observe", "").Code)
	var workers sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 6)
	for i := 0; i < 6; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); responses <- fenceRecoveryRequest(s, "POST", path+"/observe", "first") }()
	}
	workers.Wait()
	close(responses)
	for result := range responses {
		require.Equal(t, 202, result.Code, result.Body.String())
	}
	got = fenceRecoveryRequest(s, "POST", path+"/observe", "first")
	require.Equal(t, 202, got.Code, got.Body.String())
	var accepted struct {
		CommandID string `json:"command_id"`
	}
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &accepted))
	require.NotEmpty(t, accepted.CommandID)
	claim, ok, err := store.AcquireAgentCommandLease(ctx, "agent", accepted.CommandID, "observer", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, controlplane.RunRecoveryObserve, claim.RecoveryMode)
	require.Equal(t, fence.Request.ID, claim.ActionID)
	s.agentSvc = newAgentService(s)
	authorization := remoteRecoveryRequest(t, s, "POST", "/v1/agents/agent/commands/"+claim.ID+"/recovery-authorize", controlplane.AgentCommandEvent{CommandID: claim.ID, LeaseOwner: claim.LeaseOwner, Attempt: claim.Attempt})
	require.Equal(t, 200, authorization.Code, authorization.Body.String())
	require.Contains(t, authorization.Body.String(), "\"mode\":\"observe\"")
	require.Equal(t, 202, fenceRecoveryRequest(s, "POST", path+"/observe", "first").Code, "retry after lease must not replace command")
	report := &controlplane.RunRecoveryReport{CommandID: claim.ID, RunID: run.ID, AgentID: "agent", QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: run.Attempt, RunLeaseOwner: run.LeaseOwner, Mode: controlplane.RunRecoveryObserve, TopologyUID: before.TopologyUID, StateSerial: before.Meta.Serial, ObservedAt: time.Now().UTC(), Resources: []controlplane.RecoveredResource{{ActionID: actions[0].ID, Resource: actions[0].Resource.String(), OwnerUID: actions[0].OwnerUID, ResourceUID: actions[0].ResourceUID, Generation: actions[0].Generation, Status: "absent", JournalStatus: string(actions[0].Status)}}}
	_, err = store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: claim.ID, AgentID: "agent", Type: claim.Type, ActionID: claim.ActionID, Status: "completed", LeaseOwner: claim.LeaseOwner, Attempt: claim.Attempt, Recovery: report})
	require.NoError(t, err)
	cold := stateDeleteTestServer(t, store)
	require.Equal(t, 202, fenceRecoveryRequest(cold, "POST", path+"/observe", "first").Code, "terminal replay reads durable receipt")
	got = fenceRecoveryRequest(cold, "GET", path+"/recovery-plan?observation="+claim.ID, "")
	require.Equal(t, 200, got.Code, got.Body.String())
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &plan))
	require.Equal(t, "matched", plan.ObservationStatus)
	require.Equal(t, "absent", plan.Actions[0].ObservedPresence)
	require.False(t, plan.ReleaseAllowed)
	require.False(t, plan.ReplayAllowed)
	require.Contains(t, plan.Blockers, "observation_not_host_boot_attested")
	after, afterActions, err := repo.LoadStateAndActions(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, actions, afterActions)
	retained, err := store.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, *run, *retained)
	// Persisted worker reports still need exact original resource coverage.
	for _, wrong := range []string{"identity", "timestamp", "serial", "external"} {
		t.Run("observation-"+wrong, func(t *testing.T) {
			got := fenceRecoveryRequest(s, "POST", path+"/observe", wrong)
			require.Equal(t, 202, got.Code, got.Body.String())
			var next struct {
				CommandID string `json:"command_id"`
			}
			require.NoError(t, json.Unmarshal(got.Body.Bytes(), &next))
			lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", next.CommandID, "observer", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			altered := *report
			altered.CommandID = lease.ID
			altered.Resources = append([]controlplane.RecoveredResource(nil), report.Resources...)
			switch wrong {
			case "identity":
				altered.Resources[0].ResourceUID = "foreign"
			case "timestamp":
				altered.ObservedAt = fence.AcceptedAt.Add(-time.Minute)
			case "serial":
				altered.StateSerial++
			case "external":
				altered.Resources[0].Status = "unknown"
			}
			_, err = store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: lease.ID, AgentID: "agent", Type: lease.Type, ActionID: lease.ActionID, Status: "completed", LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt, Recovery: &altered})
			require.NoError(t, err)
			got = fenceRecoveryRequest(cold, "GET", path+"/recovery-plan?observation="+lease.ID, "")
			require.Equal(t, 200, got.Code, got.Body.String())
			var current hostFenceRecoveryPlan
			require.NoError(t, json.Unmarshal(got.Body.Bytes(), &current))
			if wrong == "external" {
				require.Equal(t, "matched", current.ObservationStatus)
				require.Equal(t, "unknown", current.Actions[0].ObservedPresence)
			} else {
				require.Equal(t, "mismatched", current.ObservationStatus)
				require.Contains(t, current.Blockers, "observation_identity_or_coverage_mismatch")
			}
			require.False(t, current.ReleaseAllowed)
			require.False(t, current.ReplayAllowed)
		})
	}
	// A later journal observation cannot be silently rebound to the original fence.
	changed := actions[0]
	changed.Status = runtime.ActionUncertain
	changed.Error = "diagnostic-only"
	require.NoError(t, repo.CommitActionOutcome(ctx, changed, nil))
	got = fenceRecoveryRequest(cold, "GET", path+"/recovery-plan?observation="+claim.ID, "")
	require.Equal(t, 200, got.Code, got.Body.String())
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &plan))
	require.False(t, plan.JournalMatches)
	require.Equal(t, "stale", plan.ObservationStatus)
	require.Contains(t, plan.Blockers, "observation_snapshot_changed")
	require.Contains(t, plan.Blockers, "original_journal_changed")
	require.Equal(t, 202, fenceRecoveryRequest(cold, "POST", path+"/observe", "first").Code, "existing idempotency outcome remains readable after journal drift")
	require.Equal(t, 409, fenceRecoveryRequest(cold, "POST", path+"/observe", "second").Code)
}
