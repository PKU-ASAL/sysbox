package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestFenceDeleteReconciliationClosesOnlyAnchoredAbsentNodes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := NewFilesystemExecutionGrantRepository(root)
	session, err := repo.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	grant, err := repo.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: "original", ActionID: "run", Attempt: 1, Operation: "run_destroy", Digest: strings.Repeat("a", 64), TargetBootID: "11111111-1111-4111-8111-111111111111", Scope: controlplane.ExecutionScope{TopologyUID: "owner"}}, time.Minute)
	require.NoError(t, err)
	req := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: "run", LabID: "disposable", ControllerKey: pub, JournalSHA256: strings.Repeat("b", 64), Grant: *grant}
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Op: "destroy", Operation: "destroy", Status: controlplane.RunRunning, Attempt: 1, QueuedAt: time.Now().UTC().Add(-time.Minute), Revision: "revision"}
	a := newAggregate()
	a.Serial = 1
	a.State.TopologyUID = "owner"
	action := runtime.ActionRecord{ID: "delete", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, OwnerUID: "owner", ResourceUID: "node", Generation: 1, Resource: address.Resource("sysbox_node", "web"), Action: controlplane.PlanActionDelete, Revision: 1, Status: runtime.ActionUncertain, ProviderOperationID: "delete", ProviderOperationAnchor: "action:delete", Quiescence: "unknown", Candidate: &state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "docker", ExternalID: "external", OwnerUID: "owner", ResourceUID: "node", Generation: 1}}
	a.Actions[action.ID] = action
	a.State.AddResource(*action.Candidate)
	_, journal, err := FenceJournal(run, a.Serial, actionValues(a))
	require.NoError(t, err)
	req.JournalSHA256 = journal
	_, err = repo.BeginHostFence(ctx, req)
	require.NoError(t, err)
	digest, err := req.Digest()
	require.NoError(t, err)
	receipt, err := controlplane.SignHostFenceReceipt(key, req, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: req.LabID, OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "12", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, repo.RecordHostFence(ctx, req.ID, receipt))
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	report := controlplane.RunRecoveryReport{CommandID: "observe", RunID: run.ID, AgentID: "agent", RunAttempt: run.Attempt, QueuedAt: run.QueuedAt, Revision: run.Revision, Mode: controlplane.RunRecoveryObserve, TopologyUID: "owner", StateSerial: a.Serial, ObservedAt: time.Now().UTC(), Resources: []controlplane.RecoveredResource{{ActionID: action.ID, Resource: action.Resource.String(), OwnerUID: "owner", ResourceUID: "node", Generation: 1, ExternalID: "external", Status: "absent", ProviderScope: "local-only", DeleteResidueAbsent: true, JournalStatus: "uncertain"}}}
	report, err = controlplane.SignFenceObservation(hostKey, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: receipt.NewBootID}, "observer", 1)
	require.NoError(t, err)
	cmd := CommandRecord{Retired: true, Command: controlplane.AgentCommand{ID: "observe", AgentID: "agent", Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryObserve, ActionID: req.ID, TopologyUID: "owner", ExpectedSerial: a.Serial, Run: &run, Status: controlplane.AgentCommandStatusCompleted, LeaseOwner: "observer", Attempt: 1}, Recovery: &report}

	original := CommandRecord{Command: controlplane.AgentCommand{ID: grant.CommandID, AgentID: run.AgentID, ActionID: run.ID, Type: "run_assigned", Run: &run, TopologyUID: "owner", Attempt: 1, Status: controlplane.AgentCommandStatusRunning}}
	request := HostFenceReleaseRequest{FenceID: req.ID, Key: "reconcile", ObservationCommandID: cmd.Command.ID}
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	authority, err := repo.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	transition, err := PrepareFenceDelete(authority, raw, run, original, cmd, hostPub, request, time.Now().UTC())
	require.NoError(t, err)
	require.NotEqual(t, authority, transition.PreparedAuthority)
	result, err := decodeAggregate(transition.Lifecycle)
	require.NoError(t, err)
	require.Empty(t, result.State.Resources)
	require.Equal(t, runtime.ActionSucceeded, result.Actions[action.ID].Status)
	require.Equal(t, "proven", result.Actions[action.ID].Quiescence)
	require.Equal(t, controlplane.RunFailed, transition.Run.Status)
	require.False(t, transition.Run.Recoverable)
	require.True(t, transition.Command.Retired)
	prepared, err := decodeExecutionAuthority(transition.PreparedAuthority)
	require.NoError(t, err)
	require.Equal(t, controlplane.GrantRevoked, prepared.Grants[grantScopeKey(grant.Scope)].State)
	finished, err := FinishFenceDelete(transition.PreparedAuthority, transition.Lifecycle, transition.Run, transition.Command, request)
	require.NoError(t, err)
	final, err := decodeExecutionAuthority(finished)
	require.NoError(t, err)
	require.Equal(t, controlplane.GrantStopped, final.Grants[grantScopeKey(grant.Scope)].State)
	require.True(t, final.hostFenceBlocks("agent"), "reconciliation retains hold until explicit release")
	for _, broken := range []string{"orphan-stop", "nonhex-hash"} {
		t.Run(broken, func(t *testing.T) {
			var ledger executionAuthority
			require.NoError(t, json.Unmarshal(finished, &ledger))
			record := ledger.Fences[req.ID]
			if broken == "orphan-stop" {
				record.Reconciliation = nil
			} else {
				record.Reconciliation.BeforeLifecycle = strings.Repeat("z", 64)
			}
			ledger.Fences[req.ID] = record
			encoded, err := json.Marshal(ledger)
			require.NoError(t, err)
			_, err = decodeExecutionAuthority(encoded)
			require.ErrorIs(t, err, ErrCorruptRecord)
		})
	}

	// Crash after preparing or after any postimage can only resume the same result.
	for _, post := range []bool{false, true} {
		life, r, c := raw, run, original
		if post {
			life, r, c = transition.Lifecycle, transition.Run, transition.Command
		}
		again, err := PrepareFenceDelete(transition.PreparedAuthority, life, r, c, cmd, nil, request, time.Now().UTC())
		require.NoError(t, err)
		require.Equal(t, transition.Lifecycle, again.Lifecycle)
		require.Equal(t, transition.Run, again.Run)
	}
	retry, err := PrepareFenceDelete(finished, transition.Lifecycle, transition.Run, transition.Command, cmd, nil, request, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, retry.Completed)
	for _, variant := range []string{"anchor", "identity", "present", "remote", "signature", "other-command", "child", "residue"} {
		t.Run(variant, func(t *testing.T) {
			var copy aggregate
			require.NoError(t, json.Unmarshal(raw, &copy))
			r := run
			o := original
			c := cmd
			reportCopy := report
			reportCopy.Resources = append([]controlplane.RecoveredResource(nil), report.Resources...)
			c.Recovery = &reportCopy
			auth := authority
			altered := copy.Actions[action.ID]
			switch variant {
			case "anchor":
				altered.ProviderOperationAnchor = ""
			case "identity":
				altered.Candidate.ResourceUID = "foreign"
			case "present":
				reportCopy.Resources[0].Status = "present"
			case "remote":
				reportCopy.Resources[0].ProviderScope = "remote"
			case "signature":
				proof := *reportCopy.HostObservation
				proof.Signature = []byte("bad")
				reportCopy.HostObservation = &proof
			case "other-command":
				o.Command.ID = "other"
			case "child":
				var ledger executionAuthority
				require.NoError(t, json.Unmarshal(auth, &ledger))
				g := *grant
				g.Scope.ResourceUID = "child"
				g.Scope.Generation = 1
				g.ParentEpoch = grant.Epoch
				g.Epoch = ledger.Epoch + 1
				g.Operation = "guest_task"
				g.State = controlplane.GrantRevoked
				ledger.Epoch++
				ledger.Grants[grantScopeKey(g.Scope)] = g
				auth, _ = json.Marshal(ledger)
			case "residue":
				extra := *action.Candidate
				extra.ResourceUID = "extra"
				extra.Address = address.Resource("sysbox_node", "extra")
				copy.State.AddResource(extra)
			}
			copy.Actions[action.ID] = altered
			encoded, err := json.Marshal(&copy)
			require.NoError(t, err)
			_, err = PrepareFenceDelete(auth, encoded, r, o, c, hostPub, request, time.Now().UTC())
			require.Error(t, err)
		})
	}
	for _, crash := range []string{"after-lifecycle", "after-run", "after-command"} {
		t.Run(crash, func(t *testing.T) {
			crashRoot := t.TempDir()
			crashAuthority := NewFilesystemExecutionGrantRepository(crashRoot)
			require.NoError(t, crashAuthority.storage.UpdateExecutionAuthority(ctx, func([]byte) ([]byte, error) { return authority, nil }))
			life, err := OpenFilesystem(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, life.update(ctx, "lab", func(current *aggregate) error {
				var old aggregate
				require.NoError(t, json.Unmarshal(raw, &old))
				*current = old
				return nil
			}))
			durableRun, durableCommand := run, original
			load := func() (controlplane.Run, CommandRecord, CommandRecord, error) {
				return durableRun, durableCommand, cmd, nil
			}
			_, err = life.CommitFilesystemFenceDelete(ctx, crashRoot, "lab", request, hostPub, load, func(r controlplane.Run, c CommandRecord) error {
				if crash != "after-lifecycle" {
					durableRun = r
				}
				if crash == "after-command" {
					durableCommand = c
				}
				return errors.New("injected interrupted commit")
			})
			require.ErrorContains(t, err, "injected")
			pending, err := crashAuthority.ReadHostFence(ctx, req.ID)
			require.NoError(t, err)
			require.NotNil(t, pending.Reconciliation)
			require.False(t, pending.Reconciliation.Completed)
			g, err := crashAuthority.InspectGrant(ctx, "agent", grant.Scope)
			require.NoError(t, err)
			require.Equal(t, controlplane.GrantRevoked, g.State)
			cold, err := OpenFilesystem(life.backend.(*fileBackend).root)
			require.NoError(t, err)
			finished, err := cold.CommitFilesystemFenceDelete(ctx, crashRoot, "lab", request, nil, load, func(r controlplane.Run, c CommandRecord) error { durableRun = r; durableCommand = c; return nil })
			require.NoError(t, err)
			require.True(t, finished.Completed)
			g, err = crashAuthority.InspectGrant(ctx, "agent", grant.Scope)
			require.NoError(t, err)
			require.Equal(t, controlplane.GrantStopped, g.State)
		})
	}
	// Release consumes original signed preimage, never a rewritten signature.
	release := request
	release.Key = "release"
	released, _, err := ApplyHostFenceRelease(finished, transition.Lifecycle, transition.Run, cmd, hostPub, release, time.Now().UTC())
	require.NoError(t, err)
	ledger, err := decodeExecutionAuthority(released)
	require.NoError(t, err)
	require.False(t, ledger.hostFenceBlocks("agent"))
}
