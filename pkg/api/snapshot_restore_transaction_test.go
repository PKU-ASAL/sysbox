package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestSnapshotRestoreTransactionRequiresBarrierAndReplaysReceipt(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		switch typed := store.(type) {
		case *controlstore.Local:
			s.runsDir = typed.RunsDir
		case *controlstore.SQLite:
			s.stateBackend = "sqlite://" + typed.DbPath
		case *controlstore.Postgres:
			s.stateBackend = typed.DSN
		}
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
		require.NoError(t, err)
		request, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore", 0)
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		digest, err := request.Digest()
		require.NoError(t, err)
		barrier, err := authority.BeginMaintenance(ctx, request.ID, request.TopologyUID, digest)
		require.NoError(t, err)
		request.MaintenanceEpoch = barrier.Epoch
		wrong := request
		wrong.MaintenanceEpoch++
		_, err = s.commitSnapshotRestore(ctx, "lab", wrong)
		require.Error(t, err)
		var first persistence.SnapshotRestoreReceipt
		var commitErr error
		if local, ok := store.(*controlstore.Local); ok {
			aggregateLock := flock.New(filepath.Join(local.RunsDir, "lab", "state.json.lifecycle", "lab.lock"))
			require.NoError(t, aggregateLock.Lock())
			defer aggregateLock.Close()
			type result struct {
				receipt persistence.SnapshotRestoreReceipt
				err     error
			}
			done := make(chan result, 1)
			go func() { receipt, err := s.commitSnapshotRestore(ctx, "lab", request); done <- result{receipt, err} }()
			probe := flock.New(filepath.Join(local.RunsDir, ".run-locks", "records.lock"))
			require.Eventually(t, func() bool {
				held, err := probe.TryLock()
				if err != nil {
					return false
				}
				if held {
					_ = probe.Unlock()
				}
				return !held
			}, time.Second, time.Millisecond)
			require.NoError(t, probe.Close())
			limited, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
			releaseErr := authority.EndMaintenance(limited, *barrier)
			cancel()
			require.ErrorIs(t, releaseErr, context.DeadlineExceeded)
			require.NoError(t, aggregateLock.Unlock())
			finished := <-done
			first, commitErr = finished.receipt, finished.err
		} else if postgres, ok := store.(*controlstore.Postgres); ok {
			pool, err := postgres.PoolFor(ctx)
			require.NoError(t, err)
			blocker, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer blocker.Rollback(ctx)
			_, err = blocker.Exec(ctx, "SELECT topology FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", "lab")
			require.NoError(t, err)
			type result struct {
				receipt persistence.SnapshotRestoreReceipt
				err     error
			}
			done := make(chan result, 1)
			go func() { receipt, err := s.commitSnapshotRestore(ctx, "lab", request); done <- result{receipt, err} }()
			require.Eventually(t, func() bool {
				probe, err := pool.Begin(ctx)
				if err != nil {
					return false
				}
				defer probe.Rollback(ctx)
				_, err = probe.Exec(ctx, "SELECT id FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE NOWAIT")
				var pgerr *pgconn.PgError
				return errors.As(err, &pgerr) && pgerr.Code == "55P03"
			}, time.Second, time.Millisecond)
			limited, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
			releaseErr := authority.EndMaintenance(limited, *barrier)
			cancel()
			require.Error(t, releaseErr, "release cannot overtake restore")
			require.NoError(t, blocker.Rollback(ctx))
			finished := <-done
			first, commitErr = finished.receipt, finished.err
		} else {
			first, commitErr = s.commitSnapshotRestore(ctx, "lab", request)
		}
		require.NoError(t, commitErr)
		require.Equal(t, request.ExpectedSerial+1, first.Serial)
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
		current, err := manager.Load()
		require.NoError(t, err)
		current.Lineage = "successor"
		require.NoError(t, manager.Save(current))
		before, err := manager.Load()
		require.NoError(t, err)
		require.NoError(t, repo.StateBackend("lab").(state.SnapshotDeleter).DeleteSnapshot(ctx, snapshot.ID))
		second, err := s.commitSnapshotRestore(ctx, "lab", request)
		require.NoError(t, err)
		require.Equal(t, first, second)
		after, err := manager.Load()
		require.NoError(t, err)
		require.Equal(t, before.Meta.Serial, after.Meta.Serial)
		require.Equal(t, "successor", after.Lineage)
	})
}
