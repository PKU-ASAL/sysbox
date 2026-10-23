package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func resetSSHDispositionFixture(t *testing.T) (*aggregate, runtime.ActionRecord, controlplane.IntentDispositionDecision) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	cfg := substrate.SSHAccessConfig{User: "ubuntu", AccessID: "login", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}}
	source := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "source", OwnerUID: "owner", ResourceUID: "node", Generation: 1}
	require.NoError(t, source.SetProviderState(json.RawMessage(`{"identity":"source"}`)))
	next := source
	next.Generation = 2
	next.ExternalID = "replacement"
	require.NoError(t, next.SetProviderState(json.RawMessage(`{"identity":"replacement"}`)))
	makeSSH := func(id, node string, resourceGeneration, nodeGeneration int64) state.Resource {
		digest, err := (substrate.SSHAccessMutation{ID: id, Config: cfg}).Digest()
		require.NoError(t, err)
		return state.Resource{Address: address.Resource("sysbox_ssh_access", "login"), Driver: "managed", SchemaVersion: 2, OwnerUID: "owner", ResourceUID: "ssh", Generation: resourceGeneration, ExternalID: "login", Attributes: state.MustAttributes(map[string]any{"ssh_access": map[string]any{"version": 1, "node": source.Address.String(), "config": cfg, "handle": substrate.SSHAccessHandle{NodeID: node, ID: id, Digest: digest, User: cfg.User, AccessID: cfg.AccessID, Revision: 1, BootID: "guest", TopologyUID: "owner", ResourceUID: "node", Generation: nodeGeneration, Epoch: 1}}})}
	}
	depID := fmt.Sprintf("%x", sha256.Sum256([]byte("reset-ssh\x00reset\x00sysbox_ssh_access.login")))
	previous := makeSSH("old-ssh", source.ExternalID, 1, 1)
	result := makeSSH(depID, next.ExternalID, 2, 2)
	a := newAggregate()
	a.State.TopologyUID = "owner"
	a.Serial = 1
	a.State.Resources = []state.Resource{source, previous}
	action := runtime.ActionRecord{ID: "reset", Topology: "lab", RunID: "run", RunQueuedAt: time.Now().UTC(), RunRevision: "revision", Resource: source.Address, Action: controlplane.PlanActionReset, Phase: "verifying", ReplayForbidden: true, EffectComplete: true, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "node", Generation: 2, ExpectedSerial: 1, Candidate: &next, Replacement: &next, ResetSSH: []runtime.ResetSSHDependency{{Previous: previous, DesiredDigest: "desired", ActionID: depID, Config: cfg, Started: true, Result: &result}}}
	a.Actions[action.ID] = action
	d := controlplane.IntentDispositionDecision{Version: 1, ID: "decision", FenceID: "fence", Topology: "lab", RunID: action.RunID, RunQueuedAt: action.RunQueuedAt, RunRevision: action.RunRevision, Reason: "retain observed new node and committed SSH after lost parent publication", ExpectedSerial: a.Serial, Actions: []controlplane.IntentDispositionAction{{ActionID: action.ID, Decision: "retain", ResourceUID: action.ResourceUID, Generation: action.Generation, ExternalID: next.ExternalID}}}
	require.NoError(t, action.ValidateResetSSH(a.State))
	return a, action, d
}
func TestResetSSHDispositionPublishesNodeAndSSHAtomically(t *testing.T) {
	for _, variant := range []string{"exact", "changed-source", "changed-ssh", "missing-ssh", "no-result", "other-action-same-ssh"} {
		t.Run(variant, func(t *testing.T) {
			a, action, d := resetSSHDispositionFixture(t)
			switch variant {
			case "changed-source":
				a.State.Resources[0].ExternalID = "substitute"
			case "changed-ssh":
				a.State.Resources[1].Generation++
			case "missing-ssh":
				a.State.Resources = a.State.Resources[:1]
			case "no-result":
				action.ResetSSH[0].Result = nil
				a.Actions[action.ID] = action
			case "other-action-same-ssh":
				extra := action
				extra.ID = "ssh-action"
				extra.Resource = action.ResetSSH[0].Previous.Address
				extra.Action = controlplane.PlanActionRead
				extra.Candidate = &action.ResetSSH[0].Previous
				extra.Replacement = nil
				extra.ResetSSH = nil
				extra.Generation = 1
				extra.ResourceUID = "ssh"
				a.Actions[extra.ID] = extra
				d.Actions = append(d.Actions, controlplane.IntentDispositionAction{ActionID: extra.ID, Decision: "retain", ResourceUID: "ssh", Generation: 1, ExternalID: "login"})
			}
			before, _ := json.Marshal(a)
			archived, err := intentDispositionPostimage(a, d, time.Now().UTC())
			if variant != "exact" {
				require.Error(t, err)
				after, _ := json.Marshal(a)
				require.JSONEq(t, string(before), string(after))
				return
			}
			require.NoError(t, err)
			require.Len(t, a.State.Resources, 2)
			require.Equal(t, "replacement", a.State.FindResource(action.Resource).ExternalID)
			require.Equal(t, int64(2), a.State.FindResource(action.ResetSSH[0].Previous.Address).Generation)
			require.Equal(t, state.ResourceUnknown, a.State.FindResource(action.Resource).Status)
			require.Equal(t, evidenceHash(action.ResetSSH), evidenceHash(archived[action.ID].ResetSSH))
			require.Equal(t, runtime.ActionUncertain, a.Actions[action.ID].Status)
		})
	}
}

func TestResetSSHDispositionBindsWholeSignedSnapshotAndRecoversPreparedWrite(t *testing.T) {
	testResetSSHSignedDisposition(t, false)
}
func TestResetSSHDispositionPublishesLostCommittedResultFromSignedObservation(t *testing.T) {
	testResetSSHSignedDisposition(t, true)
}
func TestResetSSHUnknownDispositionSignedCrashRecovery(t *testing.T) {
	testResetSSHSignedDisposition(t, true, true)
}
func testResetSSHSignedDisposition(t *testing.T, lost bool, quarantine ...bool) {
	unknown := len(quarantine) > 0 && quarantine[0]
	attached := len(quarantine) > 1 && quarantine[1]
	missingCreate := len(quarantine) > 2 && quarantine[2]
	ctx := context.Background()
	a, action, decision := resetSSHDispositionFixture(t)
	if attached {
		oldAttachment := state.Attachment{Name: "lan", Node: action.Resource, Network: address.Resource("sysbox_network", "lan"), Driver: action.Candidate.Driver, DriverState: json.RawMessage(`{"generation":1}`)}
		a.State.Resources[0].Attachments = []state.Attachment{oldAttachment}
		candidate := *action.Candidate
		nextAttachment := oldAttachment
		nextAttachment.DriverState = json.RawMessage(`{"generation":2}`)
		candidate.Attachments = []state.Attachment{nextAttachment}
		action.Candidate = &candidate
		action.Replacement = &candidate
	}
	discovered := action.Candidate
	if missingCreate {
		candidate := *discovered
		candidate.Attributes = state.MustAttributes(map[string]any{"desired_hash": "desired"})
		discovered = &candidate
		action.Action = controlplane.PlanActionCreate
		action.Phase = ""
		action.EffectComplete = false
		action.DesiredDigest = "desired"
		action.Candidate = nil
		action.Replacement = nil
		action.ResetSSH = nil
		a.State.Resources = nil
		a.Actions[action.ID] = action
	}
	var recorded *state.Resource
	if len(action.ResetSSH) > 0 {
		recorded = action.ResetSSH[0].Result
	}
	if unknown {
		decision.Actions[0].Decision = "retain-incomplete"
	}
	if lost && !missingCreate {
		action.ResetSSH[0].Result = nil
		a.Actions[action.ID] = action
	}
	run := controlplane.Run{ID: action.RunID, Topology: action.Topology, Revision: action.RunRevision, QueuedAt: action.RunQueuedAt, AgentID: "agent", Attempt: 1, Status: controlplane.RunRunning, Op: "reset", Operation: "reset"}
	_, journal, err := FenceJournal(run, a.Serial, actionValues(a))
	require.NoError(t, err)
	decision.JournalSHA256 = journal
	authority := NewFilesystemExecutionGrantRepository(t.TempDir())
	session, err := authority.OpenSession(ctx, "agent", "host", "")
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: a.State.TopologyUID}, AgentID: "agent", SessionID: session.ID, CommandID: "command", ActionID: run.ID, Attempt: 1, Operation: "run_reset", Digest: strings.Repeat("a", 64), TargetBootID: "11111111-1111-4111-8111-111111111111"}, time.Minute)
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	request := controlplane.HostFenceRequest{Version: 1, ID: decision.FenceID, Topology: run.Topology, RunID: run.ID, LabID: "lab", ControllerKey: pub, JournalSHA256: journal, Grant: *grant}
	_, err = authority.BeginHostFence(ctx, request)
	require.NoError(t, err)
	digest, err := request.Digest()
	require.NoError(t, err)
	receipt, err := controlplane.SignHostFenceReceipt(key, request, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: "lab", OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, authority.RecordHostFence(ctx, request.ID, receipt))
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	source := a.State.FindResource(action.Resource)
	if missingCreate {
		source = discovered
	}
	o := controlplane.RecoveredResource{ActionID: action.ID, Resource: action.Resource.String(), OwnerUID: action.OwnerUID, ResourceUID: action.ResourceUID, Generation: action.Generation, ExternalID: discovered.ExternalID, JournalStatus: string(action.Status), Status: "present", ProviderScope: "local-only"}
	old := o
	old.Generation = source.Generation
	old.ExternalID = source.ExternalID
	old.Status = "absent"
	old.DeleteResidueAbsent = true
	old.AttachmentResidueAbsent = true
	old.AttachmentCount = len(source.Attachments)
	o.ResetSource = &old
	if missingCreate {
		o.ResetSource = nil
		o.Discovered = discovered
	}
	for _, dep := range action.ResetSSH {
		r := dep.Result
		if lost {
			r = recorded
		}
		o.ResetSSH = append(o.ResetSSH, controlplane.RecoveredResource{ActionID: dep.ActionID, Resource: r.Address.String(), OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: r.Generation, ExternalID: r.ExternalID, JournalStatus: string(action.Status), Status: "present"})
	}
	if lost && !unknown {
		var saved struct {
			Handle substrate.SSHAccessHandle `json:"handle"`
		}
		raw, err := json.Marshal(recorded.Attributes["ssh_access"])
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &saved))
		o.ResetSSH[0].SSHCommitted = &substrate.SSHAccessResult{Handle: saved.Handle, Present: true}
	}
	if unknown && !missingCreate {
		o.ResetSSH[0].Status = "unknown"
	}
	report := controlplane.RunRecoveryReport{CommandID: "observe", RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: 1, Mode: controlplane.RunRecoveryObserve, TopologyUID: a.State.TopologyUID, StateSerial: a.Serial, ObservedAt: time.Now().UTC(), Resources: []controlplane.RecoveredResource{o}}
	report, err = controlplane.SignFenceObservation(hostKey, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: receipt.NewBootID}, "observer", 1)
	require.NoError(t, err)
	decision.ObservationSHA256 = evidenceHash(report)
	observation := CommandRecord{Retired: true, Command: controlplane.AgentCommand{ID: report.CommandID, AgentID: run.AgentID, ActionID: request.ID, Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryObserve, TopologyUID: a.State.TopologyUID, ExpectedSerial: a.Serial, Run: &run, Status: controlplane.AgentCommandStatusCompleted, LeaseOwner: "observer", Attempt: 1}, Recovery: &report}
	original := CommandRecord{Command: controlplane.AgentCommand{ID: grant.CommandID, AgentID: run.AgentID, ActionID: run.ID, Type: "run_assigned", TopologyUID: a.State.TopologyUID, Run: &run, Attempt: 1, Status: controlplane.AgentCommandStatusLeased}}
	auth, err := authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	life, err := json.Marshal(a)
	require.NoError(t, err)
	now := time.Now().UTC()
	first, err := PrepareFenceIntent(auth, life, decision, run, original, observation, hostPub, now)
	require.NoError(t, err)
	// Recreate the same postimage after a crash with only prepared authority saved.
	retry, err := PrepareFenceIntent(first.Authority, life, decision, run, original, observation, hostPub, now.Add(time.Second))
	require.NoError(t, err)
	require.JSONEq(t, string(first.Lifecycle), string(retry.Lifecycle))
	completed, err := FinishFenceIntent(first.Authority, first.Lifecycle, first.Run, first.Command, decision)
	require.NoError(t, err)
	result, err := decodeAggregate(first.Lifecycle)
	require.NoError(t, err)
	if lost && !missingCreate {
		require.Nil(t, result.Actions[action.ID].ResetSSH[0].Result)
		require.Nil(t, result.IntentDispositions[decision.ID].Actions[action.ID].ResetSSH[0].Result)
		if unknown {
			require.Nil(t, result.State.FindResource(action.ResetSSH[0].Previous.Address))
			require.True(t, result.State.FindResource(action.Resource).RecoveryRequired())
			if attached {
				require.Equal(t, action.Candidate.Attachments, result.State.FindResource(action.Resource).Attachments)
				require.Equal(t, action.Candidate.Attachments, result.IntentDispositions[decision.ID].Actions[action.ID].Candidate.Attachments)
			}
			for _, op := range []string{"apply", "reset", "destroy"} {
				err := ValidateCommandLifecycle(first.Lifecycle, "lab", controlplane.AgentCommand{Type: "run_assigned", TopologyUID: "owner", Run: &controlplane.Run{Op: op}})
				if op == "apply" {
					require.ErrorIs(t, err, ErrConflict)
				} else {
					require.NoError(t, err)
				}
			}
		} else {
			require.Equal(t, int64(2), result.State.FindResource(action.ResetSSH[0].Previous.Address).Generation)
		}
	}
	refs, err := gcReferences(result)
	require.NoError(t, err)
	sources := map[string]bool{}
	for _, ref := range refs.References {
		sources[ref.Source] = true
	}
	if missingCreate {
		require.Nil(t, result.Actions[action.ID].Candidate)
		require.Nil(t, result.IntentDispositions[decision.ID].Actions[action.ID].Candidate)
		require.True(t, result.State.FindResource(action.Resource).RecoveryRequired())
		require.True(t, sources["intent-history/decision/reset"])
	} else {
		require.True(t, sources["intent-ssh-previous/decision/reset/"+action.ResetSSH[0].ActionID])
		require.Equal(t, !unknown, sources["intent-ssh-result/decision/reset/"+action.ResetSSH[0].ActionID])
	}
	require.Equal(t, controlplane.RunFailed, first.Run.Status)
	require.Contains(t, first.Run.Err, "unknown")
	require.Equal(t, runtime.ActionUncertain, result.Actions[action.ID].Status)
	ledger, err := decodeExecutionAuthority(completed)
	require.NoError(t, err)
	require.False(t, ledger.Fences[decision.FenceID].IntentDisposition.ReleasedAt.IsZero())
	// Signed identity coverage is mandatory even if all node checks succeeded.
	changed := report
	changed.Resources = append([]controlplane.RecoveredResource(nil), report.Resources...)
	changed.Resources[0].ResetSSH = nil
	if missingCreate {
		changed.Resources[0].Discovered = nil
	}
	decision.ObservationSHA256 = evidenceHash(changed)
	require.Error(t, ValidateIntentDispositionChoice(decision, run, a.Serial, actionValues(a), changed))
}

func TestResetSSHUnknownDispositionQuarantinesNodeAndArchivesChildren(t *testing.T) {
	a, action, d := resetSSHDispositionFixture(t)
	action.ResetSSH[0].Result = nil
	a.Actions[action.ID] = action
	d.Actions[0].Decision = "retain-incomplete"
	original, _ := json.Marshal(action)
	archived, err := intentDispositionPostimage(a, d, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, a.State.Resources, 1)
	node := a.State.FindResource(action.Resource)
	require.Equal(t, "replacement", node.ExternalID)
	require.True(t, node.RecoveryRequired())
	require.Equal(t, state.ResourceUnknown, node.Status)
	require.Nil(t, a.State.FindResource(action.ResetSSH[0].Previous.Address))
	after, _ := json.Marshal(archived[action.ID])
	require.JSONEq(t, string(original), string(after))
	require.Nil(t, a.Actions[action.ID].ResetSSH[0].Result)
	require.Equal(t, runtime.ActionUncertain, a.Actions[action.ID].Status)
}

func TestResetSSHCompositeAttachmentsRetainReplacementObligation(t *testing.T) {
	testResetSSHSignedDisposition(t, true, true, true)
}

func TestMissingCreateIdentitySignedDispositionPreservesOriginalJournal(t *testing.T) {
	testResetSSHSignedDisposition(t, false, true, false, true)
}
