package persistence

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestFencedIntentDispositionRequiresCanonicalStopAndSignedObservation(t *testing.T) {
	testFencedIntentDisposition(t, "")
}
func TestFencedIncompleteResourceDispositionRequiresRebuild(t *testing.T) {
	for _, mode := range []string{"create", "replacement", "attachments"} {
		t.Run(mode, func(t *testing.T) { testFencedIntentDisposition(t, mode) })
	}
}
func testFencedIntentDisposition(t *testing.T, mode string) {
	ctx := context.Background()
	authority := NewFilesystemExecutionGrantRepository(t.TempDir())
	session, err := authority.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Op: "apply", Operation: "apply", Status: controlplane.RunRunning, Attempt: 1, QueuedAt: time.Now().UTC(), Revision: "revision"}
	a := runtime.ActionRecord{ID: "action", Topology: "lab", Revision: 1, ExpectedSerial: 3, RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "node"), Action: model.PlanActionCreate, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", Private: json.RawMessage(`{"identity":"fixture"}`), ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}}
	if mode != "" {
		a.Candidate.Private = nil
		require.NoError(t, a.Candidate.SetProviderState(json.RawMessage(`{"identity":"fixture"}`)))
		a.ReplayForbidden = true
		if mode == "attachments" {
			a.Candidate.Attachments = []state.Attachment{{Name: "eth0", Node: a.Resource, Network: address.Resource("sysbox_network", "lan"), Driver: "docker", MAC: "02:00:00:00:00:01"}}
		}
		if mode == "replacement" {
			a.Action = model.PlanActionReset
			a.Phase = "applying"
			a.Generation = 2
			replacement := *a.Candidate
			replacement.Generation = 2
			replacement.ExternalID = "replacement"
			a.Replacement = &replacement
		}
	}
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
	report := controlplane.RunRecoveryReport{CommandID: "observe", RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: 1, Mode: controlplane.RunRecoveryObserve, TopologyUID: "owner", StateSerial: 3, ObservedAt: time.Now().UTC(), Resources: []model.RecoveredResource{{ActionID: a.ID, Resource: a.Resource.String(), OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: 1, ExternalID: "external", Status: "present", JournalStatus: "uncertain", ProviderScope: "local-only"}}}
	if mode == "replacement" {
		old := report.Resources[0]
		old.Status = "absent"
		old.DeleteResidueAbsent = true
		replacement := old
		replacement.ExternalID = "replacement"
		replacement.Generation = 2
		replacement.Status = "present"
		replacement.DeleteResidueAbsent = false
		report.Resources[0].Generation = 2
		report.Resources[0].Status = "unknown"
		report.Resources[0].ResetSource = &old
		report.Resources[0].ResetReplacement = &replacement
	}
	report, err = controlplane.SignFenceObservation(hostKey, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: receipt.NewBootID}, "observer", 1)
	require.NoError(t, err)
	observation := CommandRecord{Retired: true, Command: controlplane.AgentCommand{ID: report.CommandID, AgentID: run.AgentID, ActionID: request.ID, Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryObserve, TopologyUID: "owner", ExpectedSerial: 3, Run: &run, Status: controlplane.AgentCommandStatusCompleted, LeaseOwner: "observer", Attempt: 1}, Recovery: &report}
	decision := controlplane.IntentDispositionDecision{Version: 1, ID: "decision", FenceID: request.ID, Topology: run.Topology, RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, ExpectedSerial: 3, JournalSHA256: journal, ObservationSHA256: evidenceHash(report), Reason: "retain interrupted creation", Actions: []controlplane.IntentDispositionAction{{ActionID: a.ID, Decision: "retain", ResourceUID: a.ResourceUID, Generation: 1, ExternalID: "external"}}}
	raw, err := authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	now := time.Now().UTC()
	if mode != "" {
		decision.Actions[0].Generation = a.Generation
		if mode == "replacement" {
			decision.Actions[0].ExternalID = "replacement"
		}
		require.Error(t, ValidateFencedIntentDisposition(raw, decision, run, 3, actions, observation, hostPub, now), "ordinary retention must not accept incomplete guest effects")
		decision.Actions[0].Decision = "retain-incomplete"
		if mode == "replacement" {
			decision.Actions[0].Decision = "retain-replacement-incomplete"
		}
		require.NoError(t, ValidateFencedIntentDisposition(raw, decision, run, 3, actions, observation, hostPub, now))
		life := newAggregate()
		life.State.TopologyUID = "owner"
		life.Serial = 3
		life.Actions[a.ID] = a
		before, err := json.Marshal(life)
		require.NoError(t, err)
		next, err := PrepareIntentDispositionLifecycle(raw, before, decision, run, observation, hostPub, now)
		require.NoError(t, err)
		result, err := decodeAggregate(next)
		require.NoError(t, err)
		require.Len(t, result.State.Resources, 1)
		resource := result.State.Resources[0]
		require.True(t, resource.RecoveryRequired())
		require.Equal(t, a.Generation, resource.Generation)
		require.Equal(t, decision.Actions[0].ExternalID, resource.ExternalID)
		require.Equal(t, runtime.ActionUncertain, result.Actions[a.ID].Status)
		require.ErrorIs(t, ValidateCommandLifecycle(next, "lab", controlplane.AgentCommand{Type: "run_assigned", TopologyUID: "owner", Run: &controlplane.Run{Op: "apply"}}), ErrConflict)
		require.NoError(t, ValidateCommandLifecycle(next, "lab", controlplane.AgentCommand{Type: "run_assigned", TopologyUID: "owner", Run: &controlplane.Run{Op: "reset"}}))
		for _, kind := range []string{"clear", "remove", "generation", "uid", "topology", "driver", "provider-state", "attachments"} {
			var changed aggregate
			require.NoError(t, json.Unmarshal(next, &changed))
			switch kind {
			case "attachments":
				changed.State.Resources[0].Attachments = []state.Attachment{{Name: "substituted"}}
			case "clear":
				require.NoError(t, changed.State.Resources[0].ClearRecoveryRequirement())
			case "remove":
				changed.State.Resources = nil
			case "generation":
				changed.State.Resources[0].Generation++
			case "uid":
				changed.State.Resources[0].ResourceUID = "other"
			case "driver":
				changed.State.Resources[0].Driver = "libvirt"
			case "provider-state":
				require.NoError(t, changed.State.Resources[0].SetProviderState(json.RawMessage(`{"identity":"foreign"}`)))
			case "topology":
				changed.State.TopologyUID = "other"
				changed.State.Resources = nil
				changed.Actions = map[string]runtime.ActionRecord{}
			}
			tampered, err := json.Marshal(changed)
			require.NoError(t, err)
			_, err = decodeAggregate(tampered)
			require.Error(t, err, kind+" must not erase recovery obligation")
		}
		repo, err := OpenFilesystem(t.TempDir())
		require.NoError(t, err)
		defer repo.Close()
		require.NoError(t, repo.backend.update(ctx, "lab", func(a *aggregate) error { *a = *result; return nil }))
		manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
		changed, err := manager.Load()
		require.NoError(t, err)
		changed.Resources = nil
		require.Error(t, manager.Save(changed), "CAS state save must not erase pending obligation")
		require.Error(t, repo.StateBackend("lab").(*stateBackend).Delete(ctx), "metadata deletion must not erase pending obligation")
		retained, err := manager.Load()
		require.NoError(t, err)
		require.Len(t, retained.Resources, 1)
		cleanup := runtime.ActionRecord{ID: "cleanup", Topology: "lab", RunID: "new-run", Resource: resource.Address, Action: model.PlanActionDelete, Status: runtime.ActionIntentCommitted, OwnerUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation, StateSerial: result.Serial, ExpectedSerial: result.Serial, Candidate: &resource}
		require.NoError(t, commitActionIntent(result, cleanup))
		cleanup = result.Actions[cleanup.ID]
		cleanup.Status = runtime.ActionSucceeded
		cleanup.EffectComplete = true
		require.NoError(t, applyActionOutcome(result, cleanup, &runtime.StatePatch{Resource: resource.Address.String(), Action: model.PlanActionDelete, Op: runtime.StatePatchDelete}))
		resolved, err := json.Marshal(result)
		require.NoError(t, err)
		_, err = decodeAggregate(resolved)
		require.NoError(t, err, "successful explicit deletion resolves the obligation")
		require.NoError(t, resetAggregate(result))
		resolved, err = json.Marshal(result)
		require.NoError(t, err)
		_, err = decodeAggregate(resolved)
		require.NoError(t, err, "resolved proof survives metadata retirement")

		return
	}
	require.NoError(t, ValidateFencedIntentDisposition(raw, decision, run, 3, actions, observation, hostPub, now))
	require.Error(t, ValidateFencedIntentDisposition(missingReceipt, decision, run, 3, actions, observation, hostPub, now))
	for _, kind := range []string{"signature", "delivery", "time", "unretired", "active-grant", "other-grant", "scope", "original-attempt"} {
		t.Run(kind, func(t *testing.T) {
			ledger, err := decodeExecutionAuthority(raw)
			require.NoError(t, err)
			d := decision
			o := observation
			r := report
			r.Resources = append([]model.RecoveredResource(nil), report.Resources...)
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
	t.Run("file-commit-resumes-after-dispatch-write-failure", func(t *testing.T) {
		repo, err := OpenFilesystem(t.TempDir())
		require.NoError(t, err)
		defer repo.Close()
		require.NoError(t, repo.backend.update(ctx, "lab", func(life *aggregate) error {
			life.State.TopologyUID = "owner"
			life.Serial = 3
			life.Actions[a.ID] = a
			return nil
		}))
		runsDir := t.TempDir()
		records := &fileGuestRecords{root: runsDir}
		require.NoError(t, records.updateRecord(ctx, executionAuthorityKind, "authority", func([]byte) ([]byte, error) { return raw, nil }))
		currentRun := run
		currentCommand := CommandRecord{Command: controlplane.AgentCommand{ID: grant.CommandID, AgentID: run.AgentID, ActionID: run.ID, Type: "run_assigned", TopologyUID: "owner", Run: &run, Attempt: 1, Status: controlplane.AgentCommandStatusRunning}}
		load := func() (controlplane.Run, CommandRecord, CommandRecord, error) {
			return currentRun, currentCommand, observation, nil
		}
		fail := true
		persist := func(r controlplane.Run, c CommandRecord) error {
			currentRun = r
			if fail {
				return fmt.Errorf("injected dispatch write failure")
			}
			currentCommand = c
			return nil
		}
		_, err = repo.CommitFilesystemFenceIntent(ctx, runsDir, "lab", observation.Command.ID, decision, hostPub, load, persist)
		require.ErrorContains(t, err, "injected")
		fail = false
		result, err := repo.CommitFilesystemFenceIntent(ctx, runsDir, "lab", observation.Command.ID, decision, hostPub, load, persist)
		require.NoError(t, err)
		require.False(t, result.ReleasedAt.IsZero())
		st, actions, err := repo.LoadStateAndActions(ctx, "lab")
		require.NoError(t, err)
		require.Len(t, st.Resources, 1)
		require.Equal(t, int64(4), st.Meta.Serial)
		require.Equal(t, runtime.ActionUncertain, actions[0].Status)
		require.True(t, currentCommand.Retired)
		again, err := repo.CommitFilesystemFenceIntent(ctx, runsDir, "lab", observation.Command.ID, decision, hostPub, load, persist)
		require.NoError(t, err)
		require.Equal(t, result, again)
		_, err = repo.CommitFilesystemFenceIntent(ctx, runsDir, "lab", "wrong-observation", decision, hostPub, load, persist)
		require.ErrorIs(t, err, ErrConflict)
	})

	t.Run("transaction-order", func(t *testing.T) {
		life := newAggregate()
		life.State.TopologyUID = "owner"
		life.Serial = 3
		life.Actions[a.ID] = a
		before, err := json.Marshal(life)
		require.NoError(t, err)
		original := CommandRecord{Command: controlplane.AgentCommand{ID: grant.CommandID, AgentID: run.AgentID, ActionID: run.ID, Type: "run_assigned", TopologyUID: "owner", Run: &run, Attempt: 1, Status: controlplane.AgentCommandStatusRunning}}
		transition, err := PrepareFenceIntent(raw, before, decision, run, original, observation, hostPub, now)
		require.NoError(t, err)
		prepared, err := decodeExecutionAuthority(transition.Authority)
		require.NoError(t, err)
		require.True(t, prepared.hostFenceBlocks(run.AgentID))
		require.Equal(t, controlplane.GrantRevoked, prepared.Grants[grantScopeKey(grant.Scope)].State)
		require.Equal(t, controlplane.RunFailed, transition.Run.Status)
		require.Contains(t, transition.Run.Err, "unknown")
		require.True(t, transition.Command.Retired)
		_, err = FinishFenceIntent(transition.Authority, before, transition.Run, transition.Command, decision)
		require.Error(t, err)
		finished, err := FinishFenceIntent(transition.Authority, transition.Lifecycle, transition.Run, transition.Command, decision)
		require.NoError(t, err)
		final, err := decodeExecutionAuthority(finished)
		require.NoError(t, err)
		require.False(t, final.hostFenceBlocks(run.AgentID))
		require.Equal(t, controlplane.GrantStopped, final.Grants[grantScopeKey(grant.Scope)].State)
		preparedLedger, err := decodeExecutionAuthority(transition.Authority)
		require.NoError(t, err)
		preparedLedger.ObservedAt = now.Add(500 * time.Millisecond)
		advanced, err := json.Marshal(preparedLedger)
		require.NoError(t, err)
		for _, post := range []bool{false, true} {
			currentLife, currentRun, currentCmd := before, run, original
			if post {
				currentLife, currentRun, currentCmd = transition.Lifecycle, transition.Run, transition.Command
			}
			retry, err := PrepareFenceIntent(advanced, currentLife, decision, currentRun, currentCmd, observation, hostPub, now.Add(time.Second))
			require.NoError(t, err)
			require.JSONEq(t, string(transition.Lifecycle), string(retry.Lifecycle))
			require.Equal(t, transition.Run, retry.Run)
			require.Equal(t, transition.Command, retry.Command)
		}
	})

	t.Run("durable-archive", func(t *testing.T) {
		life := newAggregate()
		life.State.TopologyUID = "owner"
		life.Serial = 3
		life.Actions[a.ID] = a
		before, err := json.Marshal(life)
		require.NoError(t, err)
		var pretty bytes.Buffer
		require.NoError(t, json.Indent(&pretty, before, "", "  "))
		before = pretty.Bytes()
		next, err := PrepareIntentDispositionLifecycle(raw, before, decision, run, observation, hostPub, now)
		require.NoError(t, err)
		decoded, err := decodeAggregate(next)
		require.NoError(t, err)
		require.Equal(t, runtime.ActionUncertain, decoded.Actions[a.ID].Status)
		require.Equal(t, "intent:decision", decoded.Actions[a.ID].DispositionID)
		require.Len(t, decoded.State.Resources, 1)
		require.Equal(t, state.ResourceUnknown, decoded.State.Resources[0].Status)
		require.Len(t, decoded.IntentDispositions, 1)
		refs, err := gcReferences(decoded)
		require.NoError(t, err)
		protected := false
		for _, ref := range refs.References {
			if ref.Source == "intent-history/decision/action" && ref.ExternalID == "external" {
				protected = true
			}
		}
		require.True(t, protected)
		altered := decoded.Actions[a.ID]
		altered.Status = runtime.ActionSucceeded
		decoded.Actions[a.ID] = altered
		corrupt, err := json.Marshal(decoded)
		require.NoError(t, err)
		_, err = decodeAggregate(corrupt)
		require.Error(t, err)
	})

	require.Error(t, authority.Authorize(ctx, *grant), "validation cannot release revoked authority")
}
