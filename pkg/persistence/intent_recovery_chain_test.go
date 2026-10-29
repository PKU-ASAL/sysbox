package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestSignedRecoveryObligationSurvivesFailedRebuildAndLaterDestroy(t *testing.T) {
	ctx := context.Background()
	authority := NewFilesystemExecutionGrantRepository(t.TempDir())
	session, err := authority.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	life := newAggregate()
	life.State.TopologyUID = "owner"
	life.Serial = 1
	resource := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "generation-1", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}
	require.NoError(t, resource.SetProviderState(json.RawMessage(`{"identity":"generation-1"}`)))
	first := runtime.ActionRecord{ID: "create", Topology: "lab", RunID: "first", RunQueuedAt: time.Now().UTC(), RunRevision: "revision", Resource: resource.Address, Action: model.PlanActionCreate, Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, StateSerial: 1, ExpectedSerial: 1, Candidate: &resource, ReplayForbidden: true}
	life.Actions[first.ID] = first
	apply := func(action runtime.ActionRecord, choice string, observation model.RecoveredResource) *aggregate {
		t.Helper()
		run := controlplane.Run{ID: action.RunID, Topology: "lab", AgentID: "agent", Op: "apply", Operation: "apply", Status: controlplane.RunRunning, Attempt: 1, QueuedAt: action.RunQueuedAt, Revision: action.RunRevision}
		if action.Action == model.PlanActionReset {
			run.Op = "reset"
			run.Operation = "reset"
		}
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: "command-" + run.ID, ActionID: run.ID, Attempt: 1, Operation: "run_" + run.Op, Digest: strings.Repeat("a", 64), TargetBootID: uuid.NewString(), Scope: controlplane.ExecutionScope{TopologyUID: "owner"}}, time.Minute)
		require.NoError(t, err)
		_, journal, err := FenceJournal(run, life.Serial, actionValues(life))
		require.NoError(t, err)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		request := controlplane.HostFenceRequest{Version: 1, ID: "fence-" + run.ID, Topology: "lab", RunID: run.ID, LabID: "lab", ControllerKey: pub, JournalSHA256: journal, Grant: *grant}
		_, err = authority.BeginHostFence(ctx, request)
		require.NoError(t, err)
		digest, err := request.Digest()
		require.NoError(t, err)
		receipt, err := controlplane.SignHostFenceReceipt(key, request, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: "lab", OldBootID: grant.TargetBootID, NewBootID: uuid.NewString(), ControllerBootID: uuid.NewString(), ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
		require.NoError(t, err)
		require.NoError(t, authority.RecordHostFence(ctx, request.ID, receipt))
		hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		report := controlplane.RunRecoveryReport{CommandID: "observe-" + run.ID, RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, RunAttempt: 1, Mode: controlplane.RunRecoveryObserve, TopologyUID: "owner", StateSerial: life.Serial, ObservedAt: time.Now().UTC(), Resources: []model.RecoveredResource{observation}}
		report, err = controlplane.SignFenceObservation(hostKey, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: receipt.NewBootID}, "observer", 1)
		require.NoError(t, err)
		observed := CommandRecord{Retired: true, Command: controlplane.AgentCommand{ID: report.CommandID, AgentID: run.AgentID, ActionID: request.ID, Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryObserve, TopologyUID: "owner", ExpectedSerial: life.Serial, Run: &run, Status: controlplane.AgentCommandStatusCompleted, LeaseOwner: "observer", Attempt: 1}, Recovery: &report}
		external := action.Candidate.ExternalID
		if action.Replacement != nil {
			external = action.Replacement.ExternalID
		}
		decision := controlplane.IntentDispositionDecision{Version: 1, ID: run.ID, FenceID: request.ID, Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, ExpectedSerial: life.Serial, JournalSHA256: journal, ObservationSHA256: evidenceHash(report), Reason: "retain for rebuild", Actions: []controlplane.IntentDispositionAction{{ActionID: action.ID, Decision: choice, ResourceUID: "resource", Generation: action.Generation, ExternalID: external}}}
		raw, err := authority.storage.ReadExecutionAuthority(ctx)
		require.NoError(t, err)
		stateRaw, err := json.Marshal(life)
		require.NoError(t, err)
		original := CommandRecord{Command: controlplane.AgentCommand{ID: grant.CommandID, AgentID: run.AgentID, ActionID: run.ID, Type: "run_assigned", TopologyUID: "owner", Run: &run, Attempt: 1, Status: controlplane.AgentCommandStatusRunning}}
		transition, err := PrepareFenceIntent(raw, stateRaw, decision, run, original, observed, hostPub, time.Now().UTC())
		require.NoError(t, err)
		final, err := FinishFenceIntent(transition.Authority, transition.Lifecycle, transition.Run, transition.Command, decision)
		require.NoError(t, err)
		require.NoError(t, authority.storage.UpdateExecutionAuthority(ctx, func([]byte) ([]byte, error) { return final, nil }))
		result, err := decodeAggregate(transition.Lifecycle)
		require.NoError(t, err)
		return result
	}
	observation := model.RecoveredResource{ActionID: first.ID, Resource: resource.Address.String(), OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExternalID: resource.ExternalID, Status: "present", JournalStatus: "uncertain", ProviderScope: "local-only"}
	life = apply(first, "retain-incomplete", observation)
	source := life.State.Resources[0]
	replacement := source
	replacement.Generation = 2
	replacement.ExternalID = "generation-2"
	require.NoError(t, replacement.SetProviderState(json.RawMessage(`{"identity":"generation-2"}`)))
	second := runtime.ActionRecord{ID: "reset", Topology: "lab", RunID: "second", RunQueuedAt: time.Now().UTC(), RunRevision: "revision", Resource: resource.Address, Action: model.PlanActionReset, Phase: "applying", Revision: 1, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 2, StateSerial: life.Serial, ExpectedSerial: life.Serial, Candidate: &source, Replacement: &replacement, ReplayForbidden: true}
	life.Actions[second.ID] = second
	old := observation
	old.ActionID = second.ID
	old.Status = "absent"
	old.DeleteResidueAbsent = true
	next := old
	next.Generation = 2
	next.ExternalID = replacement.ExternalID
	next.Status = "present"
	next.DeleteResidueAbsent = false
	combined := old
	combined.Generation = 2
	combined.Status = "unknown"
	combined.ResetSource = &old
	combined.ResetReplacement = &next
	life = apply(second, "retain-replacement-incomplete", combined)
	require.Equal(t, IntentRecoveryTransfer{DispositionID: "second", ActionID: "reset"}, life.IntentDispositions["first"].Transfers["create"])
	require.Equal(t, int64(2), life.State.Resources[0].Generation)
	require.True(t, life.State.Resources[0].RecoveryRequired())
	cleanup := runtime.ActionRecord{ID: "cleanup", Topology: "lab", RunID: "cleanup-run", Resource: resource.Address, Action: model.PlanActionDelete, Status: runtime.ActionIntentCommitted, OwnerUID: "owner", ResourceUID: "resource", Generation: 2, StateSerial: life.Serial, ExpectedSerial: life.Serial, Candidate: &life.State.Resources[0]}
	require.NoError(t, commitActionIntent(life, cleanup))
	cleanup = life.Actions[cleanup.ID]
	cleanup.Status = runtime.ActionSucceeded
	cleanup.EffectComplete = true
	require.NoError(t, applyActionOutcome(life, cleanup, &runtime.StatePatch{Resource: resource.Address.String(), Action: model.PlanActionDelete, Op: runtime.StatePatchDelete}))
	raw, err := json.Marshal(life)
	require.NoError(t, err)
	_, err = decodeAggregate(raw)
	require.NoError(t, err)
	require.NoError(t, resetAggregate(life))
	raw, err = json.Marshal(life)
	require.NoError(t, err)
	_, err = decodeAggregate(raw)
	require.NoError(t, err)
}
