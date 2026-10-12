package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func resetSSHFixture(t *testing.T) (*Executor, ActionRecord, *runtimeSSHConnection) {
	t.Helper()
	ctx, pc, n, conn := runtimeSSHFixture(t)
	old, err := (SSHAccessResourceHandler{}).Create(ctx, pc, n)
	require.NoError(t, err)
	pc.exec.state.AddResource(old)
	conn.calls = nil
	saved, err := decodeSSHState(old, pc.exec.state)
	require.NoError(t, err)
	node := pc.exec.state.Resources[0]
	for _, r := range pc.exec.state.Resources {
		if r.Address.Type == "sysbox_node" {
			node = r
		}
	}
	record := ActionRecord{ID: "reset", RunID: "run", Topology: pc.exec.topology, Resource: node.Address, Action: controlplane.PlanActionReset, OwnerUID: node.OwnerUID, ResourceUID: node.ResourceUID, Generation: node.Generation + 1, Candidate: &node, Status: ActionUncertain, Phase: "verifying", EffectComplete: true, Revision: 1}
	require.Equal(t, node.Address.String(), saved.Node)
	require.NoError(t, pc.exec.pinResetSSH(&record))
	replacement := cloneResetStateResource(node)
	replacement.Generation++
	record.Candidate = &replacement
	stored, err := cloneExecutionState(pc.exec.state)
	require.NoError(t, err)
	pc.exec.actions = &memoryActions{state: stored, actions: []ActionRecord{record}}
	conn.found.Status, conn.found.Current, conn.found.Config = "absent", nil, nil
	return pc.exec, record, conn
}

func TestResetSSHRebuildBindsReplacementAndPreservesOldUntilCommit(t *testing.T) {
	e, record, conn := resetSSHFixture(t)
	old := record.ResetSSH[0].Previous
	require.NoError(t, e.reconcileResetSSH(context.Background(), &record))
	require.Len(t, conn.calls, 1)
	dep := record.ResetSSH[0]
	require.True(t, dep.Started)
	require.NotNil(t, dep.Result)
	require.Equal(t, old.Generation+1, dep.Result.Generation)
	saved, err := decodeSSHState(*dep.Result, e.state)
	require.NoError(t, err)
	require.Equal(t, record.Generation, saved.Handle.Generation)
	require.Equal(t, dep.ActionID, saved.Handle.ID)
	require.Equal(t, old.Generation, e.state.FindResource(old.Address).Generation)
	require.NoError(t, record.ValidateResetSSH(e.state))
}

func TestResetSSHColdRecoveryObservesLostReplyWithoutReplay(t *testing.T) {
	e, record, conn := resetSSHFixture(t)
	conn.lost = true
	require.ErrorContains(t, e.reconcileResetSSH(context.Background(), &record), "reply lost")
	require.Len(t, conn.calls, 1)
	require.True(t, record.ResetSSH[0].Started)
	require.Nil(t, record.ResetSSH[0].Result)
	cold := NewExecutor(e.graph, e.state)
	cold.SetRunContext(e.topology, "continuation")
	cold.actions = e.actions
	require.NoError(t, cold.reconcileResetSSH(context.Background(), &record))
	require.Len(t, conn.calls, 1)
	require.NotNil(t, record.ResetSSH[0].Result)
}

func TestResetSSHUnknownAdmissionDoesNotReplay(t *testing.T) {
	e, record, conn := resetSSHFixture(t)
	record.ResetSSH[0].Started = true
	require.ErrorIs(t, e.reconcileResetSSH(context.Background(), &record), ErrActionUncertain)
	require.Empty(t, conn.calls)
}

func TestResetSSHObservationCannotStartUnadmittedMutation(t *testing.T) {
	e, record, conn := resetSSHFixture(t)
	require.ErrorIs(t, e.reconcileResetSSHWithEffects(context.Background(), &record, false), ErrActionUncertain)
	require.False(t, record.ResetSSH[0].Started)
	require.Empty(t, conn.calls)
}

func TestResetSSHDeleteUsesCurrentGenerationAfterAtomicReset(t *testing.T) {
	e, record, _ := resetSSHFixture(t)
	require.NoError(t, e.reconcileResetSSH(context.Background(), &record))
	result := *record.ResetSSH[0].Result
	e.state.RemoveResource(result.Address)
	e.state.AddResource(result)
	e.actions.(*memoryActions).actions = append(e.actions.(*memoryActions).actions, ActionRecord{ID: "original-ssh-create", Resource: result.Address, ResourceUID: result.ResourceUID, Generation: result.Generation - 1, Status: ActionSucceeded})
	deletion, err := e.newAction(context.Background(), controlplane.PlannedChange{Address: result.Address, Action: controlplane.PlanActionDelete})
	require.NoError(t, err)
	require.Equal(t, result.Generation, deletion.Generation)
}

func TestResetSSHRejectsLostAdmissionAndForeignResult(t *testing.T) {
	for _, scenario := range []string{"storage", "foreign", "missing", "target"} {
		t.Run(scenario, func(t *testing.T) {
			e, record, conn := resetSSHFixture(t)
			switch scenario {
			case "storage":
				e.actions.(*memoryActions).outcomeErr = errors.New("disk full")
			case "foreign":
				require.NoError(t, e.reconcileResetSSH(context.Background(), &record))
				record.ResetSSH[0].Result = nil
				conn.found.Current.Handle.ID = "foreign"
				conn.calls = nil
			case "missing":
				record.ResetSSH[0].DesiredDigest = "changed"
			case "target":
				record.Candidate = &state.Resource{}
			}
			require.Error(t, e.reconcileResetSSH(context.Background(), &record))
			require.Empty(t, conn.calls)
		})
	}
}
