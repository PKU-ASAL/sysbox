package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestConsoleSignedHostCompletionAndCancellation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		sess, cmd := consoleDispatchFixture()
		sess.CommandID = cmd.ID
		sess.TopologyUID = grantTestTopologyUID(t, s, "lab")
		sess.ResourceUID = "node-uid"
		sess.Generation = 2
		cmd.TopologyUID, cmd.ResourceUID, cmd.Generation, cmd.ActionID = sess.TopologyUID, sess.ResourceUID, sess.Generation, sess.ID
		cmd.Session = &sess
		require.NoError(t, store.CreateConsoleDispatch(ctx, sess, cmd))
		s.consoles = newConsoleSessionHubWithRecovery(store, false)
		lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		cmd = *lease
		repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := repo.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "guest"}
		grant, err := s.issueCommandGrant(ctx, "agent", cmd.ID, proof)
		require.NoError(t, err)
		task, err := guesttask.FromConsoleCommand(cmd)
		require.NoError(t, err)
		digest, err := task.Digest()
		require.NoError(t, err)
		require.Equal(t, digest, grant.Digest)
		// A cancellation source blocks starts/renewals while allowing the original result.
		require.NoError(t, s.consoles.Cancel(ctx, sess.ID, "requested", requestSubject{User: "operator"}))
		_, err = s.issueCommandGrant(ctx, "agent", cmd.ID, proof)
		require.ErrorIs(t, err, persistence.ErrConflict)
		receipt, err := controlplane.SignExecutionTaskStopReceipt(key, *grant, "host", "guest", task.ID, digest, controlplane.GuestTaskResult{ExitCodeKnown: true, ResultClass: "exit", Result: controlplane.GuestExecutionResult{ExitCode: 7, Encoding: "base64"}}, "nonce", time.Now().UTC())
		require.NoError(t, err)
		bad := receipt
		bad.TaskDigest = "changed"
		require.Error(t, s.completeConsoleFromHost(ctx, cmd, bad))
		require.NoError(t, s.completeConsoleFromHost(ctx, cmd, receipt))
		commands, err := store.ListAgentCommands(ctx, sess.AgentID)
		require.NoError(t, err)
		require.Empty(t, commands)
		final, err := s.consoles.Snapshot(sess.ID)
		require.NoError(t, err)
		require.True(t, final.TerminationConfirmed)
		require.NotEmpty(t, final.HostCompletion)
		require.Equal(t, 7, *final.ExitCode)
		require.Equal(t, controlplane.ConsoleSessionStatusClosed, final.Status)
		require.NoError(t, s.completeConsoleFromHost(ctx, cmd, receipt))
		cold := &Server{apiStore: store, consoles: newConsoleSessionHubWithRecovery(store, false), grantHostKeys: s.grantHostKeys}
		require.NoError(t, cold.completeConsoleFromHost(ctx, cmd, receipt))
		svc := &AgentService{consoles: cold.consoles}
		require.NoError(t, svc.updateConsoleSessionFromCommandEvent(cmd, controlplane.AgentCommandEvent{Status: "failed", Error: "worker disappeared"}))
		again, err := cold.consoles.Snapshot(sess.ID)
		require.NoError(t, err)
		require.Equal(t, final, again)
	})
}
