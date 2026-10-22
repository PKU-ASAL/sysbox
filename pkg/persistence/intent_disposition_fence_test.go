package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestFencedIntentDispositionRequiresCanonicalStopAndSignedObservation(t *testing.T) {
	ctx := context.Background()
	authority := NewFilesystemExecutionGrantRepository(t.TempDir())
	session, err := authority.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Op: "apply", Operation: "apply", Status: controlplane.RunRunning, Attempt: 1, QueuedAt: time.Now().UTC(), Revision: "revision"}
	a := runtime.ActionRecord{ID: "action", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "node"), Action: controlplane.PlanActionCreate, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}}
	actions := []runtime.ActionRecord{a}
	_, journal, err := FenceJournal(run, 3, actions)
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: "command", ActionID: run.ID, Attempt: 1, Operation: "run_apply", Digest: strings.Repeat("a", 64), TargetBootID: "11111111-1111-4111-8111-111111111111", Scope: controlplane.ExecutionScope{TopologyUID: "owner"}}, time.Minute)
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	request := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: run.ID, LabID: "lab", ControllerKey: pub, JournalSHA256: journal, Grant: *grant}
	_, err = authority.BeginHostFence(ctx, request)
	require.NoError(t, err)
	missingReceipt, err := authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	digest, err := request.Digest()
	require.NoError(t, err)
	receipt, err := controlplane.SignHostFenceReceipt(key, request, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: "lab", OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, authority.RecordHostFence(ctx, request.ID, receipt))
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	report := controlplane.RunRecoveryReport{CommandID: "observe", RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: 1, Mode: controlplane.RunRecoveryObserve, TopologyUID: "owner", StateSerial: 3, ObservedAt: time.Now().UTC(), Resources: []controlplane.RecoveredResource{{ActionID: a.ID, Resource: a.Resource.String(), OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: 1, ExternalID: "external", Status: "present", JournalStatus: "uncertain", ProviderScope: "local-only"}}}
	report, err = controlplane.SignFenceObservation(hostKey, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: receipt.NewBootID}, "observer", 1)
	require.NoError(t, err)
	observation := CommandRecord{Retired: true, Command: controlplane.AgentCommand{ID: report.CommandID, AgentID: run.AgentID, ActionID: request.ID, Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryObserve, TopologyUID: "owner", ExpectedSerial: 3, Run: &run, Status: controlplane.AgentCommandStatusCompleted, LeaseOwner: "observer", Attempt: 1}, Recovery: &report}
	decision := controlplane.IntentDispositionDecision{Version: 1, ID: "decision", FenceID: request.ID, Topology: run.Topology, RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, ExpectedSerial: 3, JournalSHA256: journal, ObservationSHA256: evidenceHash(report), Reason: "retain interrupted creation", Actions: []controlplane.IntentDispositionAction{{ActionID: a.ID, Decision: "retain", ResourceUID: a.ResourceUID, Generation: 1, ExternalID: "external"}}}
	raw, err := authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, ValidateFencedIntentDisposition(raw, decision, run, 3, actions, observation, hostPub, now))
	require.Error(t, ValidateFencedIntentDisposition(missingReceipt, decision, run, 3, actions, observation, hostPub, now))
	for _, kind := range []string{"signature", "delivery", "time", "unretired", "active-grant", "other-grant", "scope", "original-attempt"} {
		t.Run(kind, func(t *testing.T) {
			ledger, err := decodeExecutionAuthority(raw)
			require.NoError(t, err)
			d := decision
			o := observation
			r := report
			r.Resources = append([]controlplane.RecoveredResource(nil), report.Resources...)
			o.Recovery = &r
			signingKey := hostPub
			clock := now
			originalRun := run
			switch kind {
			case "signature":
				signingKey = pub
			case "delivery":
				o.Command.LeaseOwner = "wrong"
			case "time":
				clock = receipt.ObservedAt.Add(-time.Second)
			case "unretired":
				o.Retired = false
			case "active-grant":
				g := ledger.Grants[grantScopeKey(grant.Scope)]
				g.State = controlplane.GrantActive
				ledger.Grants[grantScopeKey(grant.Scope)] = g
			case "other-grant":
				g := *grant
				g.Scope.ResourceUID = "child"
				g.Scope.Generation = 1
				g.State = controlplane.GrantRevoked
				g.Epoch++
				ledger.Epoch = g.Epoch
				ledger.Grants[grantScopeKey(g.Scope)] = g
			case "scope":
				r.TopologyUID = "foreign"
				d.ObservationSHA256 = evidenceHash(r)
			case "original-attempt":
				originalRun.Attempt++
			}
			altered, err := json.Marshal(ledger)
			require.NoError(t, err)
			require.Error(t, ValidateFencedIntentDisposition(altered, d, originalRun, 3, actions, o, signingKey, clock))
		})
	}
	require.Error(t, authority.Authorize(ctx, *grant), "validation cannot release revoked authority")
}
