package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceAndGrantAdmissionSerialize(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := repo.OpenSession(ctx, "agent", "session", "")
		require.NoError(t, err)
		request := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: session.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "host"}
		var grant *controlplane.ExecutionGrant
		var barrier *persistence.MaintenanceBarrier
		var grantErr, barrierErr error
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		go func() { defer workers.Done(); <-start; grant, grantErr = repo.Issue(ctx, request, time.Minute) }()
		go func() {
			defer workers.Done()
			<-start
			barrier, barrierErr = repo.BeginMaintenance(ctx, "restore", "topology", "snapshot-digest")
		}()
		close(start)
		workers.Wait()
		require.NoError(t, barrierErr)
		cold := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		_, err = cold.Issue(ctx, request, time.Minute)
		require.ErrorIs(t, err, persistence.ErrConflict)
		if grantErr == nil {
			require.ErrorIs(t, cold.Authorize(ctx, *grant), persistence.ErrConflict)
			require.ErrorIs(t, cold.EndMaintenance(ctx, *barrier), persistence.ErrConflict)
			require.NoError(t, cold.AcceptHostStop(ctx, *grant, "verified-stop"))
		} else {
			require.ErrorIs(t, grantErr, persistence.ErrConflict)
		}
		require.NoError(t, cold.EndMaintenance(ctx, *barrier))
		next, err := cold.Issue(ctx, request, time.Minute)
		require.NoError(t, err)
		require.Greater(t, next.Epoch, barrier.Epoch)
	})
}
