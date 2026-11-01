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

func TestUnstartedConsoleRetiresDelivery(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		sess, cmd := consoleDispatchFixture()
		sess.CommandID, sess.TopologyUID, sess.ResourceUID, sess.Generation = cmd.ID, "topology", "node", 1
		cmd.Session = &sess
		cmd.TopologyUID, cmd.ResourceUID, cmd.Generation, cmd.ActionID = sess.TopologyUID, sess.ResourceUID, sess.Generation, sess.ID
		require.NoError(t, store.CreateConsoleDispatch(ctx, sess, cmd))
		hub := newConsoleSessionHubWithRecovery(store, false)
		require.NoError(t, hub.Cancel(ctx, sess.ID, "offline cancel", requestSubject{}))
		svc := &AgentService{store: store, consoles: hub}
		require.NoError(t, svc.reconcileConsoleCancellations(ctx))
		require.NoError(t, svc.reconcileConsoleCompletions(ctx))
		commands, err := store.ListAgentCommands(ctx, sess.AgentID)
		require.NoError(t, err)
		for _, command := range commands {
			require.False(t, controlstore.CommandBlocksTopology(command, sess.Topology, sess.TopologyUID), command.ID)
		}
	})
}
func TestConsoleCrashRepairsRevocation(t *testing.T) {
	for _, status := range []string{controlplane.ConsoleSessionStatusLost, controlplane.ConsoleSessionStatusCancelling} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			store := &controlstore.Local{RunsDir: t.TempDir()}
			s := stateDeleteTestServer(t, store)
			sess, cmd := consoleDispatchFixture()
			sess.CommandID = cmd.ID
			sess.TopologyUID = grantTestTopologyUID(t, s, "lab")
			sess.ResourceUID, sess.Generation = "node", 1
			cmd.Session = &sess
			cmd.TopologyUID, cmd.ResourceUID, cmd.Generation, cmd.ActionID = sess.TopologyUID, sess.ResourceUID, sess.Generation, sess.ID
			require.NoError(t, store.CreateConsoleDispatch(ctx, sess, cmd))
			lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			storage := controlstore.AuthorityStorage{Store: store}
			repo := persistence.NewExecutionGrantRepository(storage)
			session, err := repo.OpenSession(ctx, "agent", "process", "")
			require.NoError(t, err)
			grant, err := s.issueCommandGrant(ctx, "agent", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt, BootID: "guest"})
			require.NoError(t, err)
			previous, err := storage.ReadExecutionAuthority(ctx)
			require.NoError(t, err)
			hub := newConsoleSessionHubWithRecovery(store, false)
			if status == controlplane.ConsoleSessionStatusCancelling {
				require.NoError(t, hub.Cancel(ctx, sess.ID, "cancel", requestSubject{}))
			} else {
				require.NoError(t, hub.Update(sess.ID, func(x *controlplane.ConsoleSession) { recordConsoleTransportLoss(x, "disconnected") }))
			}
			// Recreate the persisted-session / prior-authority state at the filesystem crash boundary.
			require.NoError(t, storage.UpdateExecutionAuthority(ctx, func([]byte) ([]byte, error) { return previous, nil }))
			cold := newConsoleSessionHub(store)
			require.NoError(t, cold.initErr)
			svc := &AgentService{store: store, consoles: cold}
			require.NoError(t, svc.reconcileConsoleCancellations(ctx))
			require.NoError(t, svc.reconcileConsoleCompletions(ctx))
			current, err := repo.InspectGrant(ctx, "agent", grant.Scope)
			require.NoError(t, err)
			require.Equal(t, controlplane.GrantRevoked, current.State)
		})
	}
}
