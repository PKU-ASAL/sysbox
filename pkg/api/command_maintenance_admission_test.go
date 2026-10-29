package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestRawCommandAdmissionCannotBypassMaintenance(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		cmd := controlplane.AgentCommand{ID: "pending", AgentID: "agent", TopologyUID: uid, Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		active := cmd
		active.ID = "leased"
		require.NoError(t, store.SaveAgentCommand(ctx, active))
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", active.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		barrier, err := authority.BeginMaintenance(ctx, "freeze", uid, "plan")
		require.NoError(t, err)
		fresh := cmd
		fresh.ID = "fresh"
		require.ErrorIs(t, store.SaveAgentCommand(ctx, fresh), persistence.ErrConflict)
		_, ok, err = store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.False(t, ok)
		_, err = store.PrepareAgentCommandDelivery(ctx, *leased)
		require.ErrorIs(t, err, persistence.ErrConflict)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 2, "rejected admission must not publish a command")
		for _, current := range commands {
			if current.ID == cmd.ID {
				require.Zero(t, current.Attempt)
				require.Empty(t, current.LeaseOwner)
			} else {
				require.Equal(t, *leased, current, "rejected delivery cannot publish speculative progress")
			}
		}
		for _, status := range []string{"ack", "started"} {
			_, err = store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: leased.ID, AgentID: "agent", Status: status, LeaseOwner: leased.LeaseOwner, Attempt: leased.Attempt})
			require.NoError(t, err, "existing execution evidence must remain observable while frozen")
		}
		_, err = store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: leased.ID, AgentID: "agent", Status: "completed", LeaseOwner: leased.LeaseOwner, Attempt: leased.Attempt})
		require.NoError(t, err, "completion must drain during maintenance")
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID))
		stop := controlplane.AgentCommand{ID: "cancel", AgentID: "agent", Type: "cancel_command", TopologyUID: uid}
		require.NoError(t, store.SaveAgentCommand(ctx, stop))
		_, ok, err = store.AcquireAgentCommandLease(ctx, "agent", stop.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		require.NoError(t, store.SaveAgentCommand(ctx, fresh))
	})
}

func TestRawCommandWithoutUIDUsesConservativeMaintenanceScope(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		grantTestTopologyUID(t, s, "other")
		_, err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store}).BeginMaintenance(ctx, "freeze", uid, "plan")
		require.NoError(t, err)
		cmd := controlplane.AgentCommand{ID: "named", AgentID: "agent", Type: "run_assigned", Run: &controlplane.Run{Topology: "lab"}}
		require.ErrorIs(t, store.SaveAgentCommand(ctx, cmd), persistence.ErrConflict)
		cmd.Run = nil
		require.ErrorIs(t, store.SaveAgentCommand(ctx, cmd), persistence.ErrConflict, "unknown ownership cannot bypass a live barrier")
		cmd.Run = &controlplane.Run{Topology: "other"}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		mixed := cmd
		mixed.ID = "mixed"
		mixed.Operation.Topology = "other"
		mixed.Run = &controlplane.Run{Topology: "lab"}
		require.ErrorIs(t, store.SaveAgentCommand(ctx, mixed), persistence.ErrConflict, "a benign first payload must not hide a second frozen scope")
		observe := controlplane.AgentCommand{ID: "observe", AgentID: "agent", Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryObserve, TopologyUID: uid}
		require.NoError(t, store.SaveAgentCommand(ctx, observe))
		observe.ID, observe.RecoveryMode = "recover", controlplane.RunRecoveryRecover
		require.ErrorIs(t, store.SaveAgentCommand(ctx, observe), persistence.ErrConflict)
	})
}
