package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSnapshotRestoreSupervisorFinishesCommittedReleaseAfterRestart(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	s := NewServer(root, workspace)
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
	require.NoError(t, err)
	plan, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore", 0)
	require.NoError(t, err)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	barrier, err := authority.BeginMaintenance(ctx, plan.ID, plan.TopologyUID, mustRestoreDigest(t, plan))
	require.NoError(t, err)
	plan.MaintenanceEpoch = barrier.Epoch
	receipt, err := s.commitSnapshotRestore(ctx, "lab", plan)
	require.NoError(t, err)
	// Simulate process loss after atomic commit, before EndMaintenance.
	cold := NewServer(root, workspace)
	newSupervisor(cold, 0).Scan(ctx)
	recovered, err := authority.InspectMaintenance(ctx, plan.ID)
	require.NoError(t, err)
	require.True(t, recovered.Released)
	current, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, receipt.Serial, current.Meta.Serial)
	next, err := authority.BeginMaintenance(ctx, "uncommitted", plan.TopologyUID, "different-digest")
	require.NoError(t, err)
	newSupervisor(cold, 0).Scan(ctx)
	untouched, err := authority.InspectMaintenance(ctx, next.ID)
	require.NoError(t, err)
	require.False(t, untouched.Released, "absence of a receipt is not proof of restoration")
}

func TestCommittedRestoreRecoveryNeverInventsMissingAuthority(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
	require.NoError(t, err)
	request, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "missing-authority", 1)
	require.NoError(t, err)
	_, err = repo.CommitSnapshotRestore(ctx, "lab", request)
	require.NoError(t, err)
	require.ErrorIs(t, s.finishCommittedRestores(ctx, "lab"), persistence.ErrNotFound)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	_, err = authority.InspectMaintenance(ctx, request.ID)
	require.ErrorIs(t, err, persistence.ErrNotFound)
}
