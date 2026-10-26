package persistence

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
	"time"
)

func TestIntentDispositionPostimagePreservesUnknownAndExactResource(t *testing.T) {
	a := newAggregate()
	a.State.TopologyUID = "owner"
	a.Serial = 1
	action := runtime.ActionRecord{ID: "action", Topology: "lab", RunID: "run", RunQueuedAt: time.Now().UTC(), RunRevision: "revision", Resource: address.Resource("sysbox_node", "node"), Action: controlplane.PlanActionCreate, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExpectedSerial: 1, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", Private: json.RawMessage(`{"identity":"fixture"}`), ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}}
	a.Actions[action.ID] = action
	d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: 1, Actions: []controlplane.IntentDispositionAction{{ActionID: action.ID, Decision: "retain", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
	archive, err := intentDispositionPostimage(a, d, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, action, archive[action.ID])
	require.Equal(t, runtime.ActionUncertain, a.Actions[action.ID].Status)
	require.Equal(t, "intent:decision", a.Actions[action.ID].DispositionID)
	require.Len(t, a.State.Resources, 1)
	require.Equal(t, "external", a.State.Resources[0].ExternalID)
	require.Equal(t, state.ResourceUnknown, a.State.Resources[0].Status)
	require.Equal(t, int64(2), a.Serial)
	require.Equal(t, int64(1), a.Actions[action.ID].ExpectedSerial)
}
func TestIntentDispositionPostimageRejectsConflictsWithoutPartialMutation(t *testing.T) {
	for _, kind := range []string{"new-generation", "different-driver", "missing-private", "serial-overflow"} {
		t.Run(kind, func(t *testing.T) {
			a := newAggregate()
			a.State.TopologyUID = "owner"
			a.Serial = 1
			resource := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", Private: json.RawMessage(`{"identity":"fixture"}`), ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}
			action := runtime.ActionRecord{ID: "action", Topology: "lab", RunID: "run", Resource: resource.Address, Action: controlplane.PlanActionCreate, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExpectedSerial: 1, Candidate: &resource}
			a.Actions[action.ID] = action
			existing := resource
			switch kind {
			case "new-generation":
				existing.Generation = 2
				a.State.Resources = []state.Resource{existing}
			case "different-driver":
				existing.Driver = "libvirt"
				a.State.Resources = []state.Resource{existing}
			case "missing-private":
				resource.Private = nil
			case "serial-overflow":
				a.Serial = 9223372036854775807
			}
			before, err := json.Marshal(a)
			require.NoError(t, err)
			d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: a.Serial, Actions: []controlplane.IntentDispositionAction{{ActionID: "action", Decision: "retain", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
			_, err = intentDispositionPostimage(a, d, time.Now().UTC())
			require.Error(t, err)
			after, err := json.Marshal(a)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
}

func TestIntentDispositionPostimageRetainsOneExistingResourceDeterministically(t *testing.T) {
	a := newAggregate()
	a.State.TopologyUID = "owner"
	a.Serial = 1
	at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	resource := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", Private: json.RawMessage(`{"identity":"fixture"}`), ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1, CreatedAt: at.Add(-time.Hour)}
	a.State.Resources = []state.Resource{resource}
	a.Actions["action"] = runtime.ActionRecord{ID: "action", Topology: "lab", RunID: "run", Resource: resource.Address, Action: controlplane.PlanActionDelete, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExpectedSerial: 1, Candidate: &resource}
	d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: 1, Actions: []controlplane.IntentDispositionAction{{ActionID: "action", Decision: "retain", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	var cold aggregate
	require.NoError(t, json.Unmarshal(raw, &cold))
	_, err = intentDispositionPostimage(a, d, at)
	require.NoError(t, err)
	_, err = intentDispositionPostimage(&cold, d, at)
	require.NoError(t, err)
	first, err := json.Marshal(a)
	require.NoError(t, err)
	second, err := json.Marshal(&cold)
	require.NoError(t, err)
	require.JSONEq(t, string(first), string(second))
	require.Len(t, a.State.Resources, 1)
	require.Equal(t, resource.CreatedAt, a.State.Resources[0].CreatedAt)
	require.Equal(t, at, a.State.Resources[0].UpdatedAt)
}

func TestResetSourceDispositionPreservesOldGenerationAndReservesTargetHistory(t *testing.T) {
	a := newAggregate()
	a.State.TopologyUID = "owner"
	a.Serial = 1
	source := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", Private: json.RawMessage(`{"identity":"fixture"}`), ExternalID: "source", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}
	source.Private = nil
	require.NoError(t, source.SetProviderState(json.RawMessage(`{"identity":"fixture"}`)))
	require.NoError(t, source.SetRuntimeValue("reset_handle", "old-action-handle"))
	a.State.Resources = []state.Resource{source}
	a.Actions["reset"] = runtime.ActionRecord{ID: "reset", Topology: "lab", RunID: "run", Resource: source.Address, Action: controlplane.PlanActionReset, Phase: "destroying", ReplayForbidden: true, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 2, ExpectedSerial: 1, Candidate: &source}
	d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: 1, Actions: []controlplane.IntentDispositionAction{{ActionID: "reset", Decision: "retain", ResourceUID: "resource", Generation: 2, ExternalID: "source"}}}
	archive, err := intentDispositionPostimage(a, d, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), a.State.Resources[0].Generation)
	require.Equal(t, int64(2), archive["reset"].Generation)
	require.Equal(t, runtime.ActionUncertain, a.Actions["reset"].Status)
	require.True(t, a.Actions["reset"].ReplayForbidden)
	require.Equal(t, "intent:decision", a.Actions["reset"].DispositionID)
	require.Nil(t, a.State.Resources[0].RuntimeValue("reset_handle"))
	require.Equal(t, "old-action-handle", archive["reset"].Candidate.RuntimeValue("reset_handle"))
}

func TestIntentProviderStateComparisonPreservesIntegerIdentity(t *testing.T) {
	left, right := state.Resource{}, state.Resource{}
	require.NoError(t, left.SetProviderState(json.RawMessage(`{"identity":9007199254740992}`)))
	require.NoError(t, right.SetProviderState(json.RawMessage(`{"identity":9007199254740993}`)))
	require.False(t, sameIntentProviderState(left, right))
	require.NoError(t, right.SetProviderState(json.RawMessage(`{ "identity" : 9007199254740992 }`)))
	require.True(t, sameIntentProviderState(left, right))
}

func TestIncompleteDispositionRetainsAttachmentEvidence(t *testing.T) {
	a := newAggregate()
	a.State.TopologyUID = "owner"
	a.Serial = 1
	r := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1, Attachments: []state.Attachment{{Name: "eth0", Driver: "docker", Node: address.Resource("sysbox_node", "node"), Network: address.Resource("sysbox_network", "lan"), MAC: "02:00:00:00:00:01"}}}
	require.NoError(t, r.SetProviderState(json.RawMessage(`{"identity":"fixture"}`)))
	a.Actions["action"] = runtime.ActionRecord{ID: "action", Topology: "lab", RunID: "run", Resource: r.Address, Action: controlplane.PlanActionCreate, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExpectedSerial: 1, Candidate: &r}
	d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: 1, Actions: []controlplane.IntentDispositionAction{{ActionID: "action", Decision: "retain-incomplete", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
	_, err := intentDispositionPostimage(a, d, time.Now().UTC())
	require.NoError(t, err)
	retained := a.State.Resources[0]
	require.Equal(t, r.Attachments, retained.Attachments)
	require.True(t, retained.RecoveryRequired())
	changed := retained
	changed.Attachments = nil
	require.False(t, sameIntentProviderState(changed, retained), "metadata cannot discard attachment cleanup obligations")
}

func TestResetSourceDispositionKeepsPinnedSSHAndRejectsChangedDependency(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshKey, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	cfg := substrate.SSHAccessConfig{User: "root", AccessID: "login", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshKey)))}}
	digest, err := (substrate.SSHAccessMutation{ID: "original-ssh", Config: cfg}).Digest()
	require.NoError(t, err)
	source := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "source", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}
	require.NoError(t, source.SetProviderState(json.RawMessage(`{"identity":"fixture"}`)))
	previous := state.Resource{Address: address.Resource("sysbox_ssh_access", "login"), Driver: "managed", SchemaVersion: 2, OwnerUID: "owner", ResourceUID: "ssh", Generation: 1, ExternalID: "login", Attributes: state.MustAttributes(map[string]any{"ssh_access": map[string]any{"version": 1, "node": source.Address.String(), "config": cfg, "handle": substrate.SSHAccessHandle{NodeID: source.ExternalID, ID: "original-ssh", Digest: digest, User: "root", AccessID: "login", Revision: 1, BootID: "guest", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, Epoch: 1}}})}
	depID := fmt.Sprintf("%x", sha256.Sum256([]byte("reset-ssh\x00reset\x00"+previous.Address.String())))
	for _, mode := range []string{"retain", "changed", "missing", "started", "absence"} {
		t.Run(mode, func(t *testing.T) {
			a := newAggregate()
			a.State.TopologyUID = "owner"
			a.Serial = 1
			a.State.Resources = []state.Resource{source, previous}
			action := runtime.ActionRecord{ID: "reset", Topology: "lab", RunID: "run", Resource: source.Address, Action: controlplane.PlanActionReset, Phase: "destroying", ReplayForbidden: true, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 2, ExpectedSerial: 1, Candidate: &source, ResetSSH: []runtime.ResetSSHDependency{{Previous: previous, DesiredDigest: "desired", ActionID: depID, Config: cfg}}}
			require.NoError(t, action.ValidateResetSSH(a.State))
			switch mode {
			case "changed":
				a.State.Resources[1].Generation++
			case "missing":
				a.State.Resources = a.State.Resources[:1]
			case "started":
				action.ResetSSH[0].Started = true
			}
			a.Actions[action.ID] = action
			d := controlplane.IntentDispositionDecision{ID: "decision", ExpectedSerial: 1, Actions: []controlplane.IntentDispositionAction{{ActionID: "reset", Decision: "retain", ResourceUID: "resource", Generation: 2, ExternalID: "source"}}}
			if mode == "absence" {
				d.Actions[0].Decision = "absent"
			}
			before, _ := json.Marshal(a)
			archive, err := intentDispositionPostimage(a, d, time.Now().UTC())
			if mode != "retain" {
				require.Error(t, err)
				after, _ := json.Marshal(a)
				require.JSONEq(t, string(before), string(after))
				return
			}
			require.NoError(t, err)
			require.Equal(t, evidenceHash(previous), evidenceHash(*a.State.FindResource(previous.Address)))
			require.Equal(t, evidenceHash(action.ResetSSH), evidenceHash(archive[action.ID].ResetSSH))
		})
	}
}

func TestAbsentDispositionCanRemoveAttachmentMetadataOnlyWithSignedCoverage(t *testing.T) {
	a, action, d := resetSSHDispositionFixture(t)
	action.Action = controlplane.PlanActionDelete
	action.Generation = 1
	action.Phase = ""
	action.EffectComplete = false
	action.ReplayForbidden = false
	action.Candidate = &a.State.Resources[0]
	action.Candidate.Attachments = []state.Attachment{{Name: "lan", Node: action.Resource, Network: address.Resource("sysbox_network", "lan"), Driver: action.Candidate.Driver, DriverState: json.RawMessage(`{"exact":true}`)}}
	action.ResetSSH = nil
	action.Replacement = nil
	a.Actions = map[string]runtime.ActionRecord{action.ID: action}
	d.Actions[0].Decision = "absent"
	d.Actions[0].Generation = 1
	d.Actions[0].ExternalID = action.Candidate.ExternalID
	run := controlplane.Run{ID: action.RunID, Topology: action.Topology, QueuedAt: action.RunQueuedAt, Revision: action.RunRevision, AgentID: "agent", Attempt: 1}
	_, journal, err := FenceJournal(run, a.Serial, actionValues(a))
	require.NoError(t, err)
	d.JournalSHA256 = journal
	report := controlplane.RunRecoveryReport{RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: 1, Mode: controlplane.RunRecoveryObserve, TopologyUID: "owner", StateSerial: a.Serial, Resources: []controlplane.RecoveredResource{{ActionID: action.ID, Resource: action.Resource.String(), OwnerUID: action.OwnerUID, ResourceUID: action.ResourceUID, Generation: 1, ExternalID: action.Candidate.ExternalID, JournalStatus: string(action.Status), Status: "absent", ProviderScope: "local-only", DeleteResidueAbsent: true}}}
	d.ObservationSHA256 = evidenceHash(report)
	require.Error(t, ValidateIntentDispositionChoice(d, run, a.Serial, actionValues(a), report))
	report.Resources[0].AttachmentResidueAbsent = true
	report.Resources[0].AttachmentCount = 1
	d.ObservationSHA256 = evidenceHash(report)
	require.NoError(t, ValidateIntentDispositionChoice(d, run, a.Serial, actionValues(a), report))
	_, err = intentDispositionPostimage(a, d, time.Now().UTC())
	require.NoError(t, err)
	require.Nil(t, a.State.FindResource(action.Resource))
}

func TestIncompleteDispositionDoesNotNormalizeOriginalCandidate(t *testing.T) {
	a, action, d := resetSSHDispositionFixture(t)
	action.Action = controlplane.PlanActionCreate
	action.ResetSSH = nil
	action.Replacement = nil
	action.Phase = ""
	action.EffectComplete = false
	action.ReplayForbidden = true
	action.Candidate.Attributes = state.MustAttributes(map[string]any{"container_id": action.Candidate.ExternalID})
	a.State.Resources = nil
	a.Actions = map[string]runtime.ActionRecord{action.ID: action}
	d.Actions[0].Decision = "retain-incomplete"
	original := evidenceHash(action)
	_, err := intentDispositionPostimage(a, d, time.Now().UTC())
	require.NoError(t, err)
	current := a.Actions[action.ID]
	current.DispositionID = ""
	require.Equal(t, original, evidenceHash(current), "state normalization must not delete runtime attributes from original journal")
}
