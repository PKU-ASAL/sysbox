package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func checkFenceDeleteClosure(t *testing.T, s *Server, store apiStore, fence persistence.HostFenceRecord, acceptanceRoot string) {
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
	report := controlplane.RunRecoveryReport{CommandID: lease.ID, RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: run.Attempt, RunLeaseOwner: run.LeaseOwner, Mode: controlplane.RunRecoveryObserve, TopologyUID: before.TopologyUID, StateSerial: before.Meta.Serial, ObservedAt: time.Now().UTC(), Resources: []controlplane.RecoveredResource{{ActionID: actions[0].ID, Resource: actions[0].Resource.String(), OwnerUID: actions[0].OwnerUID, ResourceUID: actions[0].ResourceUID, Generation: actions[0].Generation, ExternalID: actions[0].Candidate.ExternalID, Status: "absent", ProviderScope: "local-only", DeleteResidueAbsent: true, JournalStatus: string(actions[0].Status)}}}
	report, err = controlplane.SignFenceObservation(key, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: fence.Receipt.NewBootID}, lease.LeaseOwner, lease.Attempt)
	require.NoError(t, err)
	if acceptanceRoot != "" {
		s.agentSvc = newAgentService(s)
		server := httptest.NewServer(s)
		defer server.Close()
		input := struct {
			State   any
			Serial  int64
			Actions any
			Proof   hostsupervisor.RunRecoveryRequest
			APIURL  string
			Key     []byte
		}{before, before.Meta.Serial, actions, hostsupervisor.RunRecoveryRequest{CommandID: lease.ID, LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt}, strings.Replace(server.URL, "127.0.0.1", "10.0.2.2", 1), key}
		// Only the original Run's actions are observed; prior succeeded seed is history.
		encoded, err := json.Marshal(input)
		require.NoError(t, err)
		inputPath := filepath.Join(t.TempDir(), "observer.json")
		outputPath := filepath.Join(acceptanceRoot, "delete-closure-host-observation.json")
		require.NoError(t, os.WriteFile(inputPath, encoded, 0600))
		output, err := exec.Command("/usr/bin/python3", "../../scripts/acceptance/delete_closure.py", "observe", "--root", acceptanceRoot, "--input", inputPath, "--output", outputPath, "--binary", "/tmp/sysbox-fence-observer.test").CombinedOutput()
		require.NoError(t, err, string(output))
		encoded, err = os.ReadFile(outputPath)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(encoded, &report))
		require.NoError(t, controlplane.VerifyFenceObservation(pub, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: fence.Receipt.NewBootID}, lease.LeaseOwner, lease.Attempt))
	}
	_, err = store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: lease.ID, AgentID: "agent", ActionID: lease.ActionID, Type: lease.Type, Status: "completed", LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt, Recovery: &report})
	require.NoError(t, err)
	post := func(server *Server, key string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"observation_command_id": id.CommandID})
		req := httptest.NewRequest("POST", path+"/reconcile-delete", bytes.NewReader(raw))
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
	require.Empty(t, run.EndedAt)
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
	require.NotNil(t, stored.Reconciliation)
	require.True(t, stored.Reconciliation.Completed)
	if acceptanceRoot != "" {
		raw, err := json.Marshal(stored)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(acceptanceRoot, "delete-closure-reconciled.json"), raw, 0600))
	}
	require.Nil(t, stored.Release)
	after, afterActions, err := repo.LoadStateAndActions(ctx, "lab")
	require.NoError(t, err)
	require.Empty(t, after.Resources)
	for _, a := range afterActions {
		if a.RunID == run.ID {
			require.Equal(t, "succeeded", string(a.Status))
			require.Equal(t, "proven", a.Quiescence)
			require.NotNil(t, a.QuiescenceProof)
		}
	}
	retained, err := store.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunFailed, retained.Status)
	require.False(t, retained.Recoverable)
	stopped, err := authority.InspectGrant(ctx, "agent", fence.Request.Grant.Scope)
	require.NoError(t, err)
	require.Equal(t, controlplane.GrantStopped, stopped.State)
	require.Contains(t, stopped.StopReceipt, "fence-delete:")
	original, err := s.fenceCommandStorage()
	require.NoError(t, err)
	record, err := original.ReadCommand(ctx, fence.Request.Grant.CommandID)
	require.NoError(t, err)
	require.True(t, record.Retired)
	_, ok, err = store.ClaimRun(ctx, "pending-other", "agent", "worker", time.Minute)
	require.ErrorIs(t, err, persistence.ErrConflict)
	require.False(t, ok, "reconciliation keeps hold")
	cold := stateDeleteTestServer(t, store)
	require.Equal(t, 200, post(cold, "release").Code)
	require.Equal(t, 409, post(cold, "different").Code)
	release := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"observation_command_id": id.CommandID})
		req := httptest.NewRequest("POST", path+"/release", bytes.NewReader(raw))
		req.Header.Set("X-Sysbox-Roles", "admin")
		req.Header.Set("Idempotency-Key", "explicit-release")
		w := httptest.NewRecorder()
		cold.ServeHTTP(w, req)
		return w
	}
	got = release()
	require.Equal(t, 200, got.Code, got.Body.String())
	if acceptanceRoot != "" {
		require.NoError(t, os.WriteFile(filepath.Join(acceptanceRoot, "delete-closure-released.json"), got.Body.Bytes(), 0600))
	}
	_, ok, err = store.ClaimRun(ctx, "pending-other", "agent", "next-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	// A new independent barrier is not cleared by stale retry.
	next, err := authority.BeginMaintenance(ctx, "later", before.TopologyUID, "later-digest")
	require.NoError(t, err)
	require.Equal(t, 200, release().Code)
	retainedBarrier, err := authority.InspectMaintenance(ctx, next.ID)
	require.NoError(t, err)
	require.False(t, retainedBarrier.Released)
}
