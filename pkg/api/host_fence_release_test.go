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

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
)

func checkCompletedFenceRelease(t *testing.T, s *Server, store apiStore, fence persistence.HostFenceRecord) {
	t.Helper()
	ctx := context.Background()
	path := "/v1/topologies/lab/host-fences/" + fence.Request.ID
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	before, actions, err := repo.LoadStateAndActions(ctx, "lab")
	require.NoError(t, err)
	for i, a := range actions {
		if a.RunID == fence.Request.RunID {
			actions[0], actions[i] = actions[i], actions[0]
			break
		}
	}
	run, err := store.GetRun(ctx, "run")
	require.NoError(t, err)
	require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", Status: "online", Capabilities: []string{controlplane.RunRecoveryCapability}}))
	got := fenceRecoveryRequest(s, "POST", path+"/observe", "observation")
	require.Equal(t, 202, got.Code, got.Body.String())
	var id struct {
		CommandID string `json:"command_id"`
	}
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &id))
	lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", id.CommandID, "observer", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
	fence, err = authority.ReadHostFence(ctx, fence.Request.ID)
	require.NoError(t, err)
	digest, err := fence.Request.Digest()
	require.NoError(t, err)
	report := controlplane.RunRecoveryReport{CommandID: lease.ID, RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: run.Attempt, RunLeaseOwner: run.LeaseOwner, Mode: controlplane.RunRecoveryObserve, TopologyUID: before.TopologyUID, StateSerial: before.Meta.Serial, ObservedAt: time.Now().UTC(), Resources: []model.RecoveredResource{{ActionID: actions[0].ID, Resource: actions[0].Resource.String(), OwnerUID: actions[0].OwnerUID, ResourceUID: actions[0].ResourceUID, Generation: actions[0].Generation, ExternalID: "external", Status: "absent", JournalStatus: "succeeded"}}}
	report, err = controlplane.SignFenceObservation(key, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: fence.Receipt.NewBootID}, lease.LeaseOwner, lease.Attempt)
	require.NoError(t, err)
	_, err = store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: lease.ID, AgentID: "agent", ActionID: lease.ActionID, Type: lease.Type, Status: "completed", LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt, Recovery: &report})
	require.NoError(t, err)
	post := func(server *Server, key string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"observation_command_id": id.CommandID})
		req := httptest.NewRequest("POST", path+"/release", bytes.NewReader(raw))
		req.Header.Set("X-Sysbox-Roles", "admin")
		req.Header.Set("Idempotency-Key", key)
		out := httptest.NewRecorder()
		server.ServeHTTP(out, req)
		return out
	}
	denied := httptest.NewRecorder()
	s.ServeHTTP(denied, httptest.NewRequest("POST", path+"/release", nil))
	require.Equal(t, 403, denied.Code)
	require.Equal(t, 400, post(s, "").Code)
	require.Equal(t, 409, post(s, "release").Code, "no independently confirmed host stop")
	require.NoError(t, authority.AcceptHostStop(ctx, fence.Request.Grant, "independent-stop"))
	require.Equal(t, 409, post(s, "release").Code, "pending original command must block release")
	require.NoError(t, store.DeleteAgentCommand(ctx, "agent", fence.Request.Grant.CommandID))
	var workers sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 4)
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); responses <- post(s, "release") }()
	}
	workers.Wait()
	close(responses)
	for response := range responses {
		require.Equal(t, 200, response.Code, response.Body.String())
	}
	stored, err := authority.ReadHostFence(ctx, fence.Request.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.Release)
	planResponse := fenceRecoveryRequest(s, "GET", path+"/recovery-plan", "")
	require.Equal(t, 200, planResponse.Code, planResponse.Body.String())
	require.Contains(t, planResponse.Body.String(), "\"release_status\":\"released\"")

	barrier, err := authority.InspectMaintenance(ctx, stored.BarrierID)
	require.NoError(t, err)
	require.True(t, barrier.Released)
	after, afterActions, err := repo.LoadStateAndActions(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.ElementsMatch(t, actions, afterActions)
	retained, err := store.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, *run, *retained)
	require.Equal(t, 409, fenceRecoveryRequest(s, "POST", path+"/observe", "after-release").Code, "released fence cannot authorize new observation")
	cold := stateDeleteTestServer(t, store)
	require.Equal(t, 200, post(cold, "release").Code, "replay uses retained proof even without current key config")
	require.Equal(t, 409, post(cold, "different").Code)
	_, ok, err = store.ClaimRun(ctx, "pending-other", "agent", "next-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "host hold is released")
	// A new independent barrier is not cleared by stale retry.
	next, err := authority.BeginMaintenance(ctx, "later", before.TopologyUID, "later-digest")
	require.NoError(t, err)
	require.Equal(t, 200, post(cold, "release").Code)
	retainedBarrier, err := authority.InspectMaintenance(ctx, next.ID)
	require.NoError(t, err)
	require.False(t, retainedBarrier.Released)
}
