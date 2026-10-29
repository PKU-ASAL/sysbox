package api

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestConsoleCancelConflictDoesNotPublishIntent(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		hub := newConsoleSessionHub(store)
		sess := createConsoleForTest(t, hub)
		command := persistence.ConsoleCancellationCommand(sess)
		command.Type, command.Session = "inventory", nil
		require.NoError(t, store.SaveAgentCommand(ctx, command))
		service := &ConsoleService{consoles: hub, authorize: allowConsoleForTest}
		_, err := service.Cancel(ctx, sess.ID, requestSubject{User: "operator"})
		require.ErrorIs(t, err, persistence.ErrConflict)
		stored, err := store.GetConsoleSession(ctx, sess.ID)
		require.NoError(t, err)
		require.True(t, persistence.SameConsoleSession(sess, *stored), "outbox conflict must roll back cancel intent and audit")
		st, err := hub.Get(sess.ID)
		require.NoError(t, err)
		select {
		case <-st.cancel:
			t.Fatal("failed cancel transaction cannot signal the relay")
		default:
		}
	})
}

func TestConsoleHubCancelAtomicallyPublishesOutbox(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		hub := newConsoleSessionHub(store)
		sess := createConsoleForTest(t, hub)
		require.NoError(t, hub.Cancel(context.Background(), sess.ID, "stop", requestSubject{User: "operator"}))
		commands, err := store.ListAgentCommands(context.Background(), "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1, "every persisted cancel, including timeout, carries an outbox command")
		require.Equal(t, persistence.ConsoleCancellationCommand(sess).ID, commands[0].ID)
	})
}

func TestConsoleCancelledRequestDoesNotCommitCancellation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		hub := newConsoleSessionHub(store)
		sess := createConsoleForTest(t, hub)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, hub.Cancel(ctx, sess.ID, "stop", requestSubject{User: "operator"}), context.Canceled)
		stored, err := store.GetConsoleSession(context.Background(), sess.ID)
		require.NoError(t, err)
		require.True(t, persistence.SameConsoleSession(sess, *stored))
		commands, err := store.ListAgentCommands(context.Background(), "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
	})
}

func TestConsoleFailedCancellationRepairDoesNotSignalRelay(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		hub := newConsoleSessionHub(store)
		sess := createConsoleForTest(t, hub)
		// Deliberately incomplete evidence, as may be encountered by repair.
		require.NoError(t, hub.Update(sess.ID, func(s *controlplane.ConsoleSession) { s.Status = "cancelling" }))
		cmd := persistence.ConsoleCancellationCommand(sess)
		cmd.Type, cmd.Session = "inventory", nil
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.ErrorIs(t, hub.Cancel(ctx, sess.ID, "stop", requestSubject{User: "operator"}), persistence.ErrConflict)
		st, err := hub.Get(sess.ID)
		require.NoError(t, err)
		select {
		case <-st.cancel:
			t.Fatal("cache refresh after failed cancellation must not signal relay")
		default:
		}
	})
}
