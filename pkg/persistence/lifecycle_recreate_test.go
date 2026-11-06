package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/state"
)

// A lifecycle that ran an operation always retains the pre-operation snapshot.
// Deleting it must still leave a receipt that allows a new incarnation with the
// same topology name, because snapshots are history, not live resources.
func TestDeletingLifecycleWithSnapshotsAllowsRecreation(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()

	initial, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, repo.InitializeAuthorizedTopology(ctx, "lab", initial.TopologyUID))
	require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
		a.Serial = 3
		a.Snapshots["before apply run"] = aggregateSnapshot{}
		a.UpdatedAt = time.Now().UTC()
		return nil
	}))

	current, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.EqualValues(t, 3, current.Meta.Serial)
	require.NoError(t, repo.DeleteQuiescentLifecycle(ctx, "lab", current.Meta.Serial, current.TopologyUID, nil))

	deleted, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NotEqual(t, current.TopologyUID, deleted.TopologyUID, "deletion resets the owner identity")

	require.NoError(t, repo.InitializeAuthorizedTopologyWithGuard(ctx, "lab", deleted.TopologyUID, func(receipt *LifecycleDeletionReceipt) error {
		require.NotNil(t, receipt)
		require.True(t, receipt.Empty, "snapshots are history, not live resources")
		return nil
	}))
}

// A lifecycle deleted with a live resource must never authorize a new
// incarnation under the same name: the resource may still exist on the host.
func TestDeletingLifecycleWithLiveResourceBlocksRecreation(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()

	initial, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, repo.InitializeAuthorizedTopology(ctx, "lab", initial.TopologyUID))
	require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
		a.Serial = 3
		a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), OwnerUID: a.State.TopologyUID, ResourceUID: a.State.TopologyUID + "/node-web", Generation: 1}}
		a.UpdatedAt = time.Now().UTC()
		return nil
	}))

	current, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, repo.DeleteQuiescentLifecycle(ctx, "lab", current.Meta.Serial, current.TopologyUID, nil))

	deleted, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	var receipt *LifecycleDeletionReceipt
	err = repo.InitializeAuthorizedTopologyWithGuard(ctx, "lab", deleted.TopologyUID, func(r *LifecycleDeletionReceipt) error {
		receipt = r
		return nil
	})
	require.ErrorIs(t, err, ErrConflict)
	require.NotNil(t, receipt)
	require.False(t, receipt.Empty)
}
