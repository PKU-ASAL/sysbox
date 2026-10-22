package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostFenceIntentDispositionAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true)
}
func TestHostFenceResetSourceDispositionAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, false, true)
}
func TestHostFenceResetSourceAbsenceAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, true, true)
}
func TestHostFenceIntentAbsenceAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, true)
}
func TestHostFenceIntentAbsenceDisposableVM(t *testing.T) {
	root := os.Getenv("SYSBOX_GC_FENCE_LAB")
	if root == "" {
		t.Skip("requires enrolled disposable VM fencing lab")
	}
	runHostFenceAPIContract(t, root, true, true, true, true)
}
func TestHostFenceResetSourceAbsenceDisposableVM(t *testing.T) {
	root := os.Getenv("SYSBOX_GC_FENCE_LAB")
	if root == "" {
		t.Skip("requires enrolled disposable VM fencing lab")
	}
	runHostFenceAPIContract(t, root, true, true, true, true, true)
}
func checkFenceIntentClosure(t *testing.T, s *Server, store apiStore, fence persistence.HostFenceRecord, absent bool, acceptanceRoot string) {
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
	report := controlplane.RunRecoveryReport{CommandID: lease.ID, RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: run.Attempt, RunLeaseOwner: run.LeaseOwner, Mode: controlplane.RunRecoveryObserve, TopologyUID: before.TopologyUID, StateSerial: before.Meta.Serial, ObservedAt: time.Now().UTC(), Resources: []controlplane.RecoveredResource{{ActionID: actions[0].ID, Resource: actions[0].Resource.String(), OwnerUID: actions[0].OwnerUID, ResourceUID: actions[0].ResourceUID, Generation: actions[0].Generation, ExternalID: actions[0].Candidate.ExternalID, Status: "present", ProviderScope: "local-only", DeleteResidueAbsent: true, JournalStatus: string(actions[0].Status)}}}
	if actions[0].Action == controlplane.PlanActionReset {
		old := report.Resources[0]
		old.Generation = actions[0].Candidate.Generation
		report.Resources[0].ResetSource = &old
		report.Resources[0].Status = "unknown"
	}
	if absent {
		if report.Resources[0].ResetSource != nil {
			report.Resources[0].ResetSource.Status = "absent"
		} else {
			report.Resources[0].Status = "absent"
		}
	}
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
		report = controlplane.RunRecoveryReport{}
		require.NoError(t, json.Unmarshal(encoded, &report))
		require.NoError(t, controlplane.VerifyFenceObservation(pub, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: fence.Receipt.NewBootID}, lease.LeaseOwner, lease.Attempt))
	}
	_, err = store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: lease.ID, AgentID: "agent", ActionID: lease.ActionID, Type: lease.Type, Status: "completed", LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt, Recovery: &report})
	require.NoError(t, err)
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	d := controlplane.IntentDispositionDecision{Version: 1, ID: "decision", FenceID: fence.Request.ID, Topology: run.Topology, RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, ExpectedSerial: before.Meta.Serial, JournalSHA256: fence.Request.JournalSHA256, ObservationSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Reason: "retain exact resource after failed deletion", Actions: []controlplane.IntentDispositionAction{{ActionID: actions[0].ID, Decision: "retain", ResourceUID: actions[0].ResourceUID, Generation: actions[0].Generation, ExternalID: actions[0].Candidate.ExternalID}}}
	if absent {
		d.Actions[0].Decision = "absent"
	}
	post := func(server *Server, decision controlplane.IntentDispositionDecision, admin bool) *httptest.ResponseRecorder {
		raw, err := json.Marshal(map[string]any{"decision": decision, "observation_command_id": lease.ID})
		require.NoError(t, err)
		req := httptest.NewRequest("POST", path+"/reconcile-intents", bytes.NewReader(raw))
		if admin {
			req.Header.Set("X-Sysbox-Roles", "admin")
		}
		req.Header.Set("Idempotency-Key", decision.ID)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, 403, post(s, d, false).Code)
	stale := d
	stale.ExpectedSerial++
	require.Equal(t, 409, post(s, stale, true).Code)
	result := post(s, d, true)
	require.Equal(t, 200, result.Code, result.Body.String())
	after, afterActions, err := repo.LoadStateAndActions(ctx, "lab")
	require.NoError(t, err)
	if absent {
		require.Empty(t, after.Resources)
	} else {
		require.Len(t, after.Resources, 1)
		require.Equal(t, actions[0].Candidate.ExternalID, after.Resources[0].ExternalID)
	}
	for _, action := range afterActions {
		if action.ID == actions[0].ID {
			require.Equal(t, actions[0].Status, action.Status)
			require.Equal(t, "intent:decision", action.DispositionID)
		}
	}
	ended, err := store.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunFailed, ended.Status)
	require.Contains(t, ended.Err, "unknown")
	require.Error(t, authority.Authorize(ctx, fence.Request.Grant))
	cold := stateDeleteTestServer(t, store)
	cold.grantHostKeys = s.grantHostKeys
	require.Equal(t, 200, post(cold, d, true).Code)
	planResponse := fenceRecoveryRequest(cold, "GET", path+"/recovery-plan", "")
	require.Equal(t, 200, planResponse.Code)
	var plan hostFenceRecoveryPlan
	require.NoError(t, json.Unmarshal(planResponse.Body.Bytes(), &plan))
	require.Equal(t, "completed", plan.ReconciliationStatus)
	require.Equal(t, "released", plan.ReleaseStatus)
	require.Empty(t, plan.Blockers)

	changed := d
	changed.Reason = "different"
	require.Equal(t, 409, post(cold, changed, true).Code)
	next := actions[0]
	next.ID = "new-action"
	next.RunID = "new-run"
	next.Revision = 0
	next.StateSerial = after.Meta.Serial
	next.ExpectedSerial = after.Meta.Serial
	next.Action = controlplane.PlanActionRead
	if actions[0].Action == controlplane.PlanActionReset {
		next.Generation = actions[0].Candidate.Generation
		next.Phase = ""
		next.ReplayForbidden = false
	}
	if absent {
		next.Action = controlplane.PlanActionCreate
	}
	next.Status = runtime.ActionIntentCommitted
	require.NoError(t, repo.CommitActionIntent(ctx, next))
	require.Equal(t, 200, post(cold, d, true).Code, "old decision replay must not rewrite successor")
}
