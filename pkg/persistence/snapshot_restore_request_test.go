package persistence

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestSnapshotRestoreRequestColdReplayDoesNotRewindSuccessor(t *testing.T) {
	for _, kind := range []string{"file", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			open := func() *Repository {
				var repo *Repository
				var err error
				if kind == "file" {
					repo, err = OpenFilesystem(root)
				} else {
					repo, err = OpenSQLite(filepath.Join(root, "state.db"))
				}
				require.NoError(t, err)
				return repo
			}
			repo := open()
			ctx := context.Background()
			manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
			snapshot, err := manager.Snapshot(ctx, "before")
			require.NoError(t, err)
			plan, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore-request", 7)
			require.NoError(t, err)
			first, err := repo.CommitSnapshotRestore(ctx, "lab", plan)
			require.NoError(t, err)
			require.Equal(t, plan.ExpectedSerial+1, first.Serial)
			require.NoError(t, repo.Close())
			repo = open()
			defer repo.Close()
			manager = state.NewManagerWithBackend(repo.StateBackend("lab"))
			current, err := manager.Load()
			require.NoError(t, err)
			current.Lineage = "successor"
			require.NoError(t, manager.Save(current))
			before, err := manager.Load()
			require.NoError(t, err)
			require.NoError(t, repo.StateBackend("lab").(state.SnapshotDeleter).DeleteSnapshot(ctx, snapshot.ID))
			replanned, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, plan.ID, 0)
			require.NoError(t, err)
			require.Equal(t, plan, replanned, "historical draft retry preserves the original allocated epoch")
			replay, err := repo.CommitSnapshotRestore(ctx, "lab", plan)
			require.NoError(t, err)
			require.Equal(t, first, replay)
			after, err := manager.Load()
			require.NoError(t, err)
			require.Equal(t, before.Meta.Serial, after.Meta.Serial)
			require.Equal(t, "successor", after.Lineage)
			for _, field := range []string{"snapshot", "digest", "serial", "epoch", "owner"} {
				changed := plan
				switch field {
				case "snapshot":
					changed.SnapshotID = "other"
				case "digest":
					changed.SnapshotDigest = "other"
				case "serial":
					changed.ExpectedSerial++
				case "epoch":
					changed.MaintenanceEpoch++
				case "owner":
					changed.TopologyUID = "other"
				}
				_, err := repo.CommitSnapshotRestore(ctx, "lab", changed)
				require.ErrorIs(t, err, ErrConflict, field)
			}
		})
	}
}

func TestSnapshotRestorePlanRejectsIncompatibleStateWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"lineage", "inventory", "pending", "valid"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repo, err := OpenFilesystem(t.TempDir())
			require.NoError(t, err)
			manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
			snapshot, err := manager.Snapshot(ctx, "before")
			require.NoError(t, err)
			current, err := manager.Load()
			require.NoError(t, err)
			switch scenario {
			case "lineage":
				current.Lineage = "different"
			case "inventory":
				current.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), OwnerUID: current.TopologyUID, ResourceUID: "node", Generation: 1}}
			case "pending":
				require.NoError(t, repo.CommitActionIntent(ctx, runtime.ActionRecord{ID: "pending", Topology: "lab", RunID: "run", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: current.TopologyUID, ResourceUID: "node", Generation: 1}))
			}
			if scenario == "lineage" || scenario == "inventory" {
				require.NoError(t, manager.Save(current))
			}
			before, err := manager.Load()
			require.NoError(t, err)
			_, err = repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore", 1)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrConflict)
			}
			after, err := manager.Load()
			require.NoError(t, err)
			require.Equal(t, before.Meta.Serial, after.Meta.Serial)
			require.Equal(t, before.Lineage, after.Lineage)
			require.Equal(t, before.Resources, after.Resources)
		})
	}
}

func TestSnapshotRestoreRequestsCASAndConcurrentRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := OpenSQLite(path)
	require.NoError(t, err)
	defer repo.Close()
	second, err := OpenSQLite(path)
	require.NoError(t, err)
	defer second.Close()
	ctx := context.Background()
	snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
	require.NoError(t, err)
	plan, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore", 1)
	require.NoError(t, err)
	stale := plan
	stale.ID = "different-request"
	var workers sync.WaitGroup
	results := make(chan SnapshotRestoreReceipt, 8)
	failures := make(chan error, 8)
	for i := range 8 {
		writer := repo
		if i%2 != 0 {
			writer = second
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			receipt, err := writer.CommitSnapshotRestore(ctx, "lab", plan)
			results <- receipt
			failures <- err
		}()
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var first *SnapshotRestoreReceipt
	for receipt := range results {
		if first == nil {
			copy := receipt
			first = &copy
		}
		require.Equal(t, *first, receipt)
	}
	_, err = repo.CommitSnapshotRestore(ctx, "lab", stale)
	require.ErrorIs(t, err, ErrConflict)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, plan.ExpectedSerial+1, st.Meta.Serial)
}

func TestSnapshotRestoreDraftBindsMaintenanceBeforeEpochAllocation(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
	require.NoError(t, err)
	draft, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore", 0)
	require.NoError(t, err)
	digest, err := draft.Digest()
	require.NoError(t, err)
	_, err = repo.CommitSnapshotRestore(ctx, "lab", draft)
	require.ErrorIs(t, err, ErrConflict)
	draft.MaintenanceEpoch = 7
	after, err := draft.Digest()
	require.NoError(t, err)
	require.Equal(t, digest, after, "allocated barrier binds the same preflight intent")
	changed := draft
	changed.ExpectedSerial++
	other, err := changed.Digest()
	require.NoError(t, err)
	require.NotEqual(t, digest, other)
}
