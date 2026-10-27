package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
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
	var snapshot struct {
		Topology       string    `json:"topology"`
		RunQueuedAt    time.Time `json:"run_queued_at"`
		RunRevision    string    `json:"run_revision"`
		ExpectedSerial *int64    `json:"expected_serial"`
	}
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &snapshot))
	require.Equal(t, run.Topology, snapshot.Topology)
	require.Equal(t, run.QueuedAt.UTC(), snapshot.RunQueuedAt.UTC())
	require.Equal(t, run.Revision, snapshot.RunRevision)
	require.NotNil(t, snapshot.ExpectedSerial)
	require.Equal(t, before.Meta.Serial, *snapshot.ExpectedSerial)
	require.Equal(t, "verified", plan.HostProcessExclusion)
	require.False(t, plan.ReleaseAllowed)
	require.False(t, plan.ReplayAllowed)
	require.Len(t, plan.Actions, 1)
	require.Equal(t, plan.Actions[0].ID, plan.Actions[0].Disposition.ActionID)
	require.False(t, plan.Actions[0].Disposition.ReplayAllowed)
	require.Contains(t, plan.Actions[0].Disposition.Blockers, "effect_stop_proof_required")
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
	var auth controlplane.RunRecoveryAuthorization
	require.NoError(t, json.Unmarshal(authorization.Body.Bytes(), &auth))
	require.NotNil(t, auth.FenceObservation)
	digest, err := fence.Request.Digest()
	require.NoError(t, err)
	require.Equal(t, digest, auth.FenceObservation.RequestSHA256)
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
	// Existing unresolved Run and intent must never be released by observation.
	releaseBody, _ := json.Marshal(map[string]string{"observation_command_id": claim.ID})
	releaseRequest := httptest.NewRequest("POST", path+"/release", bytes.NewReader(releaseBody))
	releaseRequest.Header.Set("X-Sysbox-Roles", "admin")
	releaseRequest.Header.Set("Idempotency-Key", "blocked-release")
	releaseResponse := httptest.NewRecorder()
	s.ServeHTTP(releaseResponse, releaseRequest)
	require.Equal(t, 409, releaseResponse.Code, releaseResponse.Body.String())

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
	// A host signature binds the new boot, complete report and exact delivery.
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cold.grantHostKeys = map[string]ed25519.PublicKey{"agent": hostPub}
	for _, variant := range []string{"valid", "host-executor", "tampered", "wrong-boot", "wrong-lease", "foreign-key"} {
		t.Run("signed-"+variant, func(t *testing.T) {
			got := fenceRecoveryRequest(s, "POST", path+"/observe", "signed-"+variant)
			require.Equal(t, 202, got.Code, got.Body.String())
			var next struct {
				CommandID string `json:"command_id"`
			}
			require.NoError(t, json.Unmarshal(got.Body.Bytes(), &next))
			lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", next.CommandID, "sign-observer", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			current := *report
			current.CommandID = lease.ID
			current.ObservedAt = time.Now().UTC()
			current.Resources = append([]controlplane.RecoveredResource(nil), report.Resources...)
			binding := *auth.FenceObservation
			owner, attempt := lease.LeaseOwner, lease.Attempt
			key := hostKey
			switch variant {
			case "wrong-boot":
				binding.HostBootID = fence.Request.Grant.TargetBootID
			case "wrong-lease":
				attempt++
			case "foreign-key":
				_, key, err = ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
			}
			signed, err := controlplane.SignFenceObservation(key, current, binding, owner, attempt)
			require.NoError(t, err)
			if variant == "host-executor" {
				server := httptest.NewServer(s)
				defer server.Close()
				host := agentexec.HostRunExecutor{Bridge: fenceObservationBridge{repository: repo}, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent", HostBootID: binding.HostBootID, HostKey: hostKey}, Authority: hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "agent", APIURL: server.URL}}}
				observed, err := host.Recover(ctx, hostsupervisor.RunRecoveryRequest{CommandID: lease.ID, LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt})
				require.NoError(t, err)
				signed = *observed
				require.Equal(t, "unknown", signed.Resources[0].Status, "no candidate identity means no provider absence claim")
			}
			if variant == "tampered" {
				signed.Resources[0].Status = "present"
			}
			_, err = store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: lease.ID, AgentID: "agent", ActionID: lease.ActionID, Type: lease.Type, Status: "completed", LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt, Recovery: &signed})
			require.NoError(t, err)
			got = fenceRecoveryRequest(cold, "GET", path+"/recovery-plan?observation="+lease.ID, "")
			require.Equal(t, 200, got.Code, got.Body.String())
			var result hostFenceRecoveryPlan
			require.NoError(t, json.Unmarshal(got.Body.Bytes(), &result))
			if variant == "valid" || variant == "host-executor" {
				require.Equal(t, "verified", result.ObservationAttestation)
				require.NotEmpty(t, result.ObservationSHA256)
				require.NotContains(t, result.Blockers, "observation_not_host_boot_attested")
				if variant == "host-executor" {
					require.Equal(t, "unknown", result.Actions[0].ObservedPresence)
				} else {
					require.Equal(t, "absent", result.Actions[0].ObservedPresence)
				}
			} else {
				require.Equal(t, "invalid", result.ObservationAttestation)
				require.Empty(t, result.ObservationSHA256)
				require.Equal(t, "unknown", result.Actions[0].ObservedPresence)
				require.Contains(t, result.Blockers, "host_observation_signature_invalid")
			}
			require.False(t, result.ReleaseAllowed)
			require.False(t, result.ReplayAllowed)
		})
	}
	pendingResponse := fenceRecoveryRequest(s, "POST", path+"/observe", "before-drift")
	require.Equal(t, 202, pendingResponse.Code, pendingResponse.Body.String())
	var pending struct {
		CommandID string `json:"command_id"`
	}
	require.NoError(t, json.Unmarshal(pendingResponse.Body.Bytes(), &pending))
	pendingLease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", pending.CommandID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	// A later journal observation cannot be silently rebound to the original fence.
	changed := actions[0]
	changed.Status = runtime.ActionUncertain
	changed.Error = "diagnostic-only"
	require.NoError(t, repo.CommitActionOutcome(ctx, changed, nil))
	got = fenceRecoveryRequest(cold, "GET", path+"/recovery-plan?observation="+claim.ID, "")
	require.Equal(t, 200, got.Code, got.Body.String())
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &plan))
	driftAuthorization := remoteRecoveryRequest(t, s, "POST", "/v1/agents/agent/commands/"+pendingLease.ID+"/recovery-authorize", controlplane.AgentCommandEvent{LeaseOwner: pendingLease.LeaseOwner, Attempt: pendingLease.Attempt})
	require.Equal(t, 409, driftAuthorization.Code, driftAuthorization.Body.String())
	require.False(t, plan.JournalMatches)
	require.Equal(t, "stale", plan.ObservationStatus)
	require.Contains(t, plan.Blockers, "observation_snapshot_changed")
	require.Contains(t, plan.Blockers, "original_journal_changed")
	require.Equal(t, 202, fenceRecoveryRequest(cold, "POST", path+"/observe", "first").Code, "existing idempotency outcome remains readable after journal drift")
	require.Equal(t, 409, fenceRecoveryRequest(cold, "POST", path+"/observe", "second").Code)
}

// The real host executor uses this canonical repository for observation only.
// All other bridge capabilities deliberately remain unavailable.
type fenceObservationBridge struct {
	agentexec.HostRunBridge
	repository runtime.ActionRepository
}

func (b fenceObservationBridge) ActionRepository(string) (runtime.ActionRepository, error) {
	return b.repository, nil
}
