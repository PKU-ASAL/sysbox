package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestRunSaveCannotBypassMaintenanceBarrier(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		barrier, err := authority.BeginMaintenance(ctx, "freeze", st.TopologyUID, "plan")
		require.NoError(t, err)
		run := controlplane.Run{ID: "run", Topology: "lab", Status: controlplane.RunQueued, Op: "apply", Operation: "apply", QueuedAt: time.Now().UTC()}
		require.ErrorIs(t, store.SaveRun(ctx, run), persistence.ErrConflict)
		_, err = store.GetRun(ctx, run.ID)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		require.NoError(t, store.SaveRun(ctx, run))
	})
}

func TestRunAuthorityRejectsEmptyPersistedObject(t *testing.T) {
	require.NoError(t, validateRunAuthority(nil, "owner"))
	require.ErrorIs(t, validateRunAuthority([]byte(`{}`), "owner"), persistence.ErrCorruptRecord)
}

func TestLocalRunSaveHoldsAuthorityThroughAppend(t *testing.T) {
	store := &controlstore.Local{RunsDir: t.TempDir()}
	s := stateDeleteTestServer(t, store)
	grantTestTopologyUID(t, s, "lab")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store}).ObserveTime(ctx))
	// There is no runs.jsonl yet, so inventory reads do not take this lock.
	// The first append must wait, after it has acquired execution authority.
	blocked, err := controlstore.LockLocalHistory(ctx, filepath.Join(store.RunsDir, "lab", "runs.jsonl"), false)
	require.NoError(t, err)
	defer blocked.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- store.SaveRun(ctx, controlplane.Run{ID: "run", Topology: "lab", Status: controlplane.RunQueued})
	}()
	probe := flock.New(filepath.Join(store.RunsDir, "_execution-authority", "authority.lock"))
	defer probe.Close()
	require.Eventually(t, func() bool {
		held, err := probe.TryLock()
		if err != nil {
			return false
		}
		if held {
			_ = probe.Unlock()
		}
		return !held
	}, time.Second, time.Millisecond, "SaveRun must hold authority while waiting to append")
	require.NoError(t, blocked.Unlock())
	require.NoError(t, <-done)
}

func TestRunCanFinishDuringMaintenanceButCannotRestart(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		run := controlplane.Run{ID: "run", Topology: "lab", Status: controlplane.RunQueued, Op: "apply", Operation: "apply", OperationKey: "key", QueuedAt: time.Now().UTC()}
		require.NoError(t, store.SaveRun(ctx, run))
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		barrier, err := authority.BeginMaintenance(ctx, "freeze", uid, "plan")
		require.NoError(t, err)
		run.Status, run.EndedAt = controlplane.RunFailed, time.Now().UTC()
		require.NoError(t, store.SaveRun(ctx, run), "terminal reports must drain work during freeze")
		next := run
		next.Status, next.EndedAt, next.QueuedAt = controlplane.RunQueued, time.Time{}, run.QueuedAt.Add(time.Second)
		_, err = store.RestartRun(ctx, next)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		_, err = store.RestartRun(ctx, next)
		require.NoError(t, err)
	})
}
