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

func TestConsoleCancelIntentRecoversOutboxAfterRestart(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		hub := newConsoleSessionHub(store)
		sess, err := hub.Create(ctx, "lab", "node", "agent", controlplane.ConsoleRequest{})
		require.NoError(t, err)
		// Inject an incomplete registry record to exercise repair. Normal Cancel
		// now commits its outbox atomically and cannot produce this window.
		previous := sess
		sess.Status = controlplane.ConsoleSessionStatusCancelling
		require.NoError(t, store.CompareAndSwapConsoleSession(ctx, previous, sess))
		cold := newConsoleSessionHub(store)
		current, err := cold.Snapshot(sess.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.ConsoleSessionStatusCancelling, current.Status)
		svc := &AgentService{store: store, consoles: cold}
		require.NoError(t, svc.reconcileConsoleCancellations(ctx))
		require.NoError(t, svc.reconcileConsoleCancellations(ctx))
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, persistence.ConsoleCancellationCommand(sess).ID, commands[0].ID)
		require.Equal(t, "cancel_command", commands[0].Type)
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", commands[0].ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, svc.reconcileConsoleCancellations(ctx))
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, *leased, commands[0], "recovery cannot reset an active delivery lease")
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", commands[0].ID))
		require.NoError(t, svc.reconcileConsoleCancellations(ctx))
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands, "retired cancellation must not be recreated")
		current, err = cold.Snapshot(sess.ID)
		require.NoError(t, err)
		require.True(t, persistence.ConsoleSessionUnresolved(current), "delivery retirement is not a remote stop receipt")
	})
}

func TestConsoleCancelRecoveryIgnoresLossAndRejectsChangedBinding(t *testing.T) {
	ctx := context.Background()
	store := &controlstore.Local{RunsDir: t.TempDir()}
	hub := newConsoleSessionHub(store)
	sess := createConsoleForTest(t, hub)
	svc := &AgentService{store: store, consoles: newConsoleSessionHub(store)}
	require.NoError(t, svc.reconcileConsoleCancellations(ctx))
	commands, err := store.ListAgentCommands(ctx, "")
	require.NoError(t, err)
	require.Empty(t, commands, "restart transport loss must not invent cancel intent")
	lost, err := svc.consoles.Snapshot(sess.ID)
	require.NoError(t, err)
	cancelling := lost
	cancelling.Status = controlplane.ConsoleSessionStatusCancelling
	cancelling.EndedAt = time.Time{}
	require.NoError(t, store.CompareAndSwapConsoleSession(ctx, lost, cancelling))
	cmd := persistence.ConsoleCancellationCommand(sess)
	cmd.Session.Node = "wrong-node"
	require.NoError(t, store.SaveAgentCommand(ctx, cmd))
	require.ErrorIs(t, svc.reconcileConsoleCancellations(ctx), persistence.ErrConflict)
	commands, err = store.ListAgentCommands(ctx, "")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Equal(t, "wrong-node", commands[0].Session.Node, "conflicting evidence must not be overwritten")
}
