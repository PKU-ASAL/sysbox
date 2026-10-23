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
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
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
func TestHostFenceIncompleteCreateAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, false, false, true)
}
func TestHostFenceIncompleteReplacementAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, false, true, false, true)
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
func TestHostFenceIncompleteReplacementDisposableVM(t *testing.T) {
	root := os.Getenv("SYSBOX_GC_FENCE_LAB")
	if root == "" {
		t.Skip("requires enrolled disposable VM fencing lab")
	}
	require.Equal(t, "libvirt", os.Getenv("SYSBOX_DELETE_CLOSURE_PROVIDER"))
	runHostFenceAPIContract(t, root, true, true, true, false, true, false, true)
}
func checkFenceIntentClosure(t *testing.T, s *Server, store apiStore, fence persistence.HostFenceRecord, absent bool, acceptanceRoot string, unknownSSH bool, children ...controlplane.ExecutionGrant) {
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
	verifiedSSH := actions[0].Phase == "verifying" && actions[0].EffectComplete && len(actions[0].ResetSSH) > 0
	if verifiedSSH {
		source := before.FindResource(actions[0].Resource)
		old := report.Resources[0]
		old.Generation = source.Generation
		old.ExternalID = source.ExternalID
		old.Status = "absent"
		old.DeleteResidueAbsent = true
		report.Resources[0].ResetSource = &old
		for _, dep := range actions[0].ResetSSH {
			r := dep.Result
			var committed *substrate.SSHAccessResult
			if r == nil {
				var previous struct {
					Handle substrate.SSHAccessHandle `json:"handle"`
				}
				raw, err := json.Marshal(dep.Previous.Attributes["ssh_access"])
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &previous))
				h := previous.Handle
				h.ID, h.NodeID, h.Generation = dep.ActionID, actions[0].Candidate.ExternalID, actions[0].Generation
				h.Digest, err = (substrate.SSHAccessMutation{ID: dep.ActionID, ExpectedRevision: h.Revision - 1, Config: dep.Config}).Digest()
				require.NoError(t, err)
				committed = &substrate.SSHAccessResult{Handle: h, Present: true}
				result := dep.Previous
				result.Generation++
				r = &result
			}
			report.Resources[0].ResetSSH = append(report.Resources[0].ResetSSH, controlplane.RecoveredResource{ActionID: dep.ActionID, Resource: r.Address.String(), OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: r.Generation, ExternalID: r.ExternalID, Status: "present", JournalStatus: string(actions[0].Status), SSHCommitted: committed})
		}
		if unknownSSH {
			for i := range report.Resources[0].ResetSSH {
				report.Resources[0].ResetSSH[i].SSHCommitted = nil
				report.Resources[0].ResetSSH[i].Status = "unknown"
			}
		}
	} else if actions[0].Action == controlplane.PlanActionReset {
		old := report.Resources[0]
		old.Generation = actions[0].Candidate.Generation
		if actions[0].Replacement != nil {
			old.Status = "absent"
			replacement := old
			replacement.Generation = actions[0].Replacement.Generation
			replacement.ExternalID = actions[0].Replacement.ExternalID
			replacement.Status = "present"
			replacement.DeleteResidueAbsent = false
			report.Resources[0].ResetReplacement = &replacement
		}
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
	incomplete := actions[0].ReplayForbidden && !actions[0].ResetBeforeReplacement() && (!verifiedSSH || unknownSSH)
	if incomplete {
		d.Actions[0].Decision = "retain-incomplete"
	}
	if actions[0].Replacement != nil && !verifiedSSH {
		d.Actions[0].Decision = "retain-replacement-incomplete"
		d.Actions[0].ExternalID = actions[0].Replacement.ExternalID
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
	if len(children) > 0 {
		require.Equal(t, 409, post(s, d, true).Code, "parent disposition must remain held while child result is unknown")
		planReply := fenceRecoveryRequest(s, "GET", path+"/recovery-plan?observation="+lease.ID, "")
		require.Contains(t, planReply.Body.String(), "child_stop_required")
		require.Contains(t, planReply.Body.String(), "setup-task")
		for _, child := range children {
			stop, err := controlplane.SignRebootedTaskStopReceipt(key, child, fence.Receipt.NewBootID, "new-guest-boot", "child-stop", time.Now().UTC())
			require.NoError(t, err)
			encoded, err := json.Marshal(stop)
			require.NoError(t, err)
			stopped := httptest.NewRecorder()
			s.ServeHTTP(stopped, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(encoded)))
			require.Equal(t, 200, stopped.Code, stopped.Body.String())
		}
	}
	result := post(s, d, true)
	require.Equal(t, 200, result.Code, result.Body.String())
	after, afterActions, err := repo.LoadStateAndActions(ctx, "lab")
	require.NoError(t, err)
	if absent {
		require.Empty(t, after.Resources)
	} else {
		count := 1
		if verifiedSSH && !unknownSSH {
			count += len(actions[0].ResetSSH)
		}
		require.Len(t, after.Resources, count)
		require.Equal(t, d.Actions[0].ExternalID, after.FindResource(actions[0].Resource).ExternalID)
		if verifiedSSH && !unknownSSH {
			for _, dep := range actions[0].ResetSSH {
				require.Equal(t, dep.Previous.Generation+1, after.FindResource(dep.Previous.Address).Generation)
			}
		}
	}
	for _, action := range afterActions {
		if action.ID == actions[0].ID {
			require.Equal(t, actions[0].Status, action.Status)
			require.Equal(t, actions[0].ResetSSH, action.ResetSSH)
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
	if unknownSSH {
		require.Nil(t, after.FindResource(actions[0].ResetSSH[0].Previous.Address))
		for _, op := range []string{"apply", "reset", "destroy"} {
			err := repo.ValidateCommandLifecycle(ctx, "lab", controlplane.AgentCommand{Type: "run_assigned", TopologyUID: after.TopologyUID, Run: &controlplane.Run{Op: op}})
			if op == "apply" {
				require.ErrorIs(t, err, persistence.ErrConflict)
			} else {
				require.NoError(t, err)
			}
		}
	}
	if incomplete {
		require.True(t, after.Resources[0].RecoveryRequired())
		proposed := *after
		proposed.Resources = nil
		manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
		require.Error(t, manager.Save(&proposed))
		require.Equal(t, 200, post(cold, d, true).Code)
		if unknownSSH {
			node := *after.FindResource(actions[0].Resource)
			cleanup := runtime.ActionRecord{ID: "explicit-cleanup", Topology: "lab", RunID: "cleanup-run", Resource: node.Address, Action: controlplane.PlanActionDelete, Status: runtime.ActionIntentCommitted, OwnerUID: node.OwnerUID, ResourceUID: node.ResourceUID, Generation: node.Generation, StateSerial: after.Meta.Serial, ExpectedSerial: after.Meta.Serial, Candidate: &node}
			require.NoError(t, repo.CommitActionIntent(ctx, cleanup))
			records, err := repo.LoadActions(ctx, "lab")
			require.NoError(t, err)
			for _, record := range records {
				if record.ID == cleanup.ID {
					cleanup = record
				}
			}
			cleanup.Status = runtime.ActionSucceeded
			cleanup.EffectComplete = true
			require.NoError(t, repo.CommitActionOutcome(ctx, cleanup, &runtime.StatePatch{Resource: node.Address.String(), Action: cleanup.Action, Op: runtime.StatePatchDelete}))
			cleaned, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.Empty(t, cleaned.Resources)
			require.NoError(t, repo.ValidateCommandLifecycle(ctx, "lab", controlplane.AgentCommand{Type: "run_assigned", TopologyUID: after.TopologyUID, Run: &controlplane.Run{Op: "apply"}}))
			require.Equal(t, 200, post(cold, d, true).Code, "old retry must not restore deleted metadata")
		}
		return
	}
	next := actions[0]
	next.ID = "new-action"
	next.RunID = "new-run"
	next.Revision = 0
	next.StateSerial = after.Meta.Serial
	next.ExpectedSerial = after.Meta.Serial
	next.Action = controlplane.PlanActionRead
	next.ResetSSH = nil
	next.Replacement = nil
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

func TestHostFenceParentDispositionWaitsForGuestChild(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, false, false, false, false, true)
}
