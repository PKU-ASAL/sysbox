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

func TestConsoleDeliveryReplayPreservesUnconfirmedExecution(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		hub := newConsoleSessionHub(store)
		sess, err := hub.Create(ctx, "lab", "node", "agent", controlplane.ConsoleRequest{})
		require.NoError(t, err)
		cmd := controlplane.AgentCommand{ID: "open", AgentID: "agent", Type: "session_open", Session: &sess, Status: controlplane.AgentCommandStatusQueued}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		event := controlplane.AgentCommandEvent{AgentID: "agent", CommandID: cmd.ID, Type: cmd.Type, Status: "completed", LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt}
		svc := &AgentService{store: store, consoles: hub}
		hub.store = failingConsoleSave{Store: store}
		require.Error(t, svc.updateCommandFromEvent(ctx, event))
		accepted, err := store.ApplyAgentCommandEvent(ctx, event)
		require.NoError(t, err)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands, "command outcome already retired durably")
		// The cold hub observes lost transport, while the original terminal
		// command remains durable and may repair the missing session update.
		svc.consoles = newConsoleSessionHub(store)
		require.NoError(t, svc.reconcileConsoleCompletions(ctx))
		completed, err := store.GetConsoleSession(ctx, sess.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.ConsoleSessionStatusLost, completed.Status)
		require.NotEmpty(t, completed.Err)
		require.False(t, completed.TerminationConfirmed)
		require.Empty(t, completed.HostCompletion)
		require.False(t, completed.EndedAt.Before(accepted.EndedAt), "delivery replay must preserve uncertainty")
		require.NoError(t, svc.reconcileConsoleCompletions(ctx), "scan retry does not create a new effect")
		for _, field := range []string{"status", "agent", "attempt", "lease", "error"} {
			bad := event
			switch field {
			case "status":
				bad.Status = "failed"
			case "agent":
				bad.AgentID = "other"
			case "attempt":
				bad.Attempt++
			case "lease":
				bad.LeaseOwner = "other"
			case "error":
				bad.Error = "changed"
			}
			require.Error(t, svc.updateCommandFromEvent(ctx, bad), field)
		}
		require.NoError(t, svc.updateCommandFromEvent(ctx, event))
		again, err := store.GetConsoleSession(ctx, sess.ID)
		require.NoError(t, err)
		require.Equal(t, completed.Status, again.Status)
		require.False(t, again.TerminationConfirmed)
	})
}

func TestConsoleRecoveryRefusesAmbiguousOrUnconfirmedOutcome(t *testing.T) {
	for _, scenario := range []string{"retired_only", "duplicate", "foreign_node", "foreign_creation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store := &controlstore.Local{RunsDir: t.TempDir()}
			hub := newConsoleSessionHub(store)
			sess := createConsoleForTest(t, hub)
			commandSession := sess
			if scenario == "foreign_node" {
				commandSession.Node = "other"
			}
			if scenario == "foreign_creation" {
				commandSession.CreatedAt = commandSession.CreatedAt.Add(time.Second)
			}
			cmd := controlplane.AgentCommand{ID: "open", AgentID: sess.AgentID, Type: "session_open", Session: &commandSession, Status: controlplane.AgentCommandStatusQueued}
			require.NoError(t, store.SaveAgentCommand(ctx, cmd))
			if scenario == "retired_only" {
				require.NoError(t, store.DeleteAgentCommand(ctx, cmd.AgentID, cmd.ID))
			} else {
				_, err := store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{AgentID: cmd.AgentID, CommandID: cmd.ID, Status: "completed"})
				require.NoError(t, err)
			}
			if scenario == "duplicate" {
				cmd.ID = "another-open"
				require.NoError(t, store.SaveAgentCommand(ctx, cmd))
			}
			svc := &AgentService{store: store, consoles: newConsoleSessionHub(store)}
			err := svc.reconcileConsoleCompletions(ctx)
			if scenario == "retired_only" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, persistence.ErrConflict)
				require.Error(t, svc.updateCommandFromEvent(ctx, controlplane.AgentCommandEvent{AgentID: "agent", CommandID: "open", Status: "completed"}), "direct replay must use the same identity checks")
			}
			current, err := store.GetConsoleSession(ctx, sess.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.ConsoleSessionStatusLost, current.Status)
			require.True(t, persistence.ConsoleSessionUnresolved(*current))
		})
	}
}
