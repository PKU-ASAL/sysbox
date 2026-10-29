package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestRestoreWaitsForTopologyActivityAcrossWorkspaceNames(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		// Give the unrelated topology its own real identity before freezing lab.
		grantTestTopologyUID(t, s, "other-topology")
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
		require.NoError(t, err)
		plan, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore", 0)
		require.NoError(t, err)
		op := controlplane.NodeOperation{ID: "busy", AgentID: "agent", Topology: "lab", Workspace: "project-workspace", Node: "vm", Operation: "pause", Status: controlplane.NodeOperationStatusQueued, CreatedAt: time.Now().UTC()}
		require.NoError(t, store.CreateNodeOperation(ctx, op))
		queued := op
		op.Status = controlplane.NodeOperationStatusRunning
		require.NoError(t, store.CompareAndSwapNodeOperation(ctx, queued, op))
		require.ErrorIs(t, s.workspaceService().EnsureTopologyQuiescent(ctx, "lab"), persistence.ErrConflict, "workspace filtering must not hide target topology activity")
		require.ErrorIs(t, s.workspaceService().ForceUnlock(ctx, "lab"), persistence.ErrConflict)
		result, err := s.restoreSnapshotWorkflow(ctx, "lab", plan)
		require.NoError(t, err)
		require.Equal(t, "waiting_for_stop", result.Status)
		require.Nil(t, result.Receipt)
		current, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, plan.ExpectedSerial, current.Meta.Serial)
		_, err = repo.ReadSnapshotRestoreReceipt(ctx, "lab", plan.ID)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		// Complete only the relevant operation. An unrelated topology sharing this
		// workspace must not keep this topology's restoration waiting forever.
		unrelated := op
		unrelated.ID, unrelated.Topology = "other", "other-topology"
		unrelated.Status = controlplane.NodeOperationStatusQueued
		require.NoError(t, store.CreateNodeOperation(ctx, unrelated))
		ended := op
		ended.Status, ended.EndedAt = controlplane.NodeOperationStatusDone, time.Now().UTC()
		require.NoError(t, store.CompareAndSwapNodeOperation(ctx, op, ended))
		require.NoError(t, s.workspaceService().EnsureTopologyQuiescent(ctx, "lab"))
		result, err = s.restoreSnapshotWorkflow(ctx, "lab", result.Request)
		require.NoError(t, err)
		require.Equal(t, "restored", result.Status)
		require.NotNil(t, result.Receipt)
	})
}
