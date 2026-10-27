package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestPostgresHostCompletionWaitsForAuthorityWithoutHoldingCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &postgresAPIStore{dsn: isolatedPostgresTestDSN(t)}
	pool, err := s.poolFor(ctx)
	require.NoError(t, err)
	defer pool.Close()
	op, cmd := nodeDispatchFixture()
	require.NoError(t, s.CreateNodeOperationDispatch(ctx, op, cmd))
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s})
	_, err = authority.OpenSession(ctx, op.AgentID, "lock-test", "")
	require.NoError(t, err)
	issuer, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer issuer.Rollback(context.Background())
	var raw []byte
	require.NoError(t, issuer.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&raw))
	var pid int
	require.NoError(t, issuer.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	done := make(chan error, 1)
	go func() {
		// An intentionally invalid receipt makes completion roll back after
		// all locks are acquired. This test targets lock acquisition, not proof.
		_, err := s.completeNodeOperation(ctx, op.AgentID, op.ID, &controlplane.SignedExecutionStopReceipt{},
			func(current controlplane.NodeOperation, _ *persistence.CommandRecord) (controlplane.NodeOperation, error) {
				return current, nil
			})
		done <- err
	}()
	joined := false
	defer func() {
		if !joined {
			cancel()
			<-done
		}
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond, "completion must actually be waiting on the held authority row")
	// Issuance already holds authority and next takes command. If completion
	// took command first, NOWAIT deterministically detects the deadlock edge.
	err = issuer.QueryRow(ctx, "SELECT data::text FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE NOWAIT", cmd.ID).Scan(&raw)
	require.NoError(t, err, "completion must not hold command while waiting for authority")
	require.NoError(t, issuer.Commit(ctx))
	completionErr := <-done
	joined = true
	require.Error(t, completionErr, "invalid proof must roll back after lock acquisition")
	stored, err := s.GetNodeOperation(ctx, op.ID)
	require.NoError(t, err)
	require.Equal(t, op, *stored, "failed proof must not change the operation")
}
