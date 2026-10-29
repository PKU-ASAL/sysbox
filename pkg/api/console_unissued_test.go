package api

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestConsoleWithoutGrantCanFinishWhileAgentIsOffline(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
			ctx := context.Background()
			sess, cmd := consoleDispatchFixture()
			sess.CommandID = cmd.ID
			sess.TopologyUID = "topology"
			sess.ResourceUID = "node"
			sess.Generation = 1
			cmd.Session = &sess
			cmd.TopologyUID = sess.TopologyUID
			cmd.ResourceUID = sess.ResourceUID
			cmd.Generation = 1
			cmd.ActionID = sess.ID
			require.NoError(t, store.CreateConsoleDispatch(ctx, sess, cmd))
			hub := newConsoleSessionHubWithRecovery(store, false)
			if cancelled {
				require.NoError(t, hub.Cancel(ctx, sess.ID, "offline cancel", requestSubject{}))
			} else {
				require.NoError(t, hub.Update(sess.ID, func(s *controlplane.ConsoleSession) {
					recordConsoleTransportLoss(s, "worker failed before authorization")
				}))
			}
			final, err := store.GetConsoleSession(ctx, sess.ID)
			require.NoError(t, err)
			require.True(t, final.NotStarted)
			require.False(t, final.TerminationConfirmed, "absence of grant is not a remote stop receipt")
			require.False(t, persistence.ConsoleSessionUnresolved(*final))
			commands, err := store.ListAgentCommands(ctx, sess.AgentID)
			require.NoError(t, err)
			require.Empty(t, commands, "offline cancellation must retire both deliveries")
			if cancelled {
				require.Equal(t, controlplane.ConsoleSessionStatusCancelled, final.Status)
			} else {
				require.Equal(t, controlplane.ConsoleSessionStatusFailed, final.Status)
			}
			service := &AgentService{store: store, consoles: hub}
			require.NoError(t, service.updateConsoleSessionFromCommandEvent(cmd, controlplane.AgentCommandEvent{Status: "started"}))
			after, err := store.GetConsoleSession(ctx, sess.ID)
			require.NoError(t, err)
			require.Equal(t, final, after)
		})
	}
}
