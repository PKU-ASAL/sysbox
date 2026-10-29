package api

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestSnapshotRestorePreservesIdenticalUnresolvedJournal(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		intent := runtime.ActionRecord{ID: "pending", Topology: "lab", RunID: "interrupted", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, ProviderOperationID: "pending", ProviderOperationAnchor: "action:pending", Quiescence: "unknown"}
		require.NoError(t, repo.CommitActionIntent(ctx, intent))
		before, err := repo.LoadActions(ctx, "lab")
		require.NoError(t, err)
		snapshots := repo.StateBackend("lab").(state.SnapshotBackend)
		snapshot, err := snapshots.Snapshot(ctx, "retain unresolved intent")
		require.NoError(t, err)
		request, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore-unresolved", 0)
		require.NoError(t, err)
		digest, err := request.Digest()
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		barrier, err := authority.BeginMaintenance(ctx, request.ID, st.TopologyUID, digest)
		require.NoError(t, err)
		request.MaintenanceEpoch = barrier.Epoch
		receipt, err := s.commitSnapshotRestore(ctx, "lab", request)
		require.NoError(t, err)
		after, actions, err := repo.LoadStateAndActions(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, before, actions, "restore must not rewind or resolve any intent")
		require.Equal(t, st.TopologyUID, after.TopologyUID)
		require.Equal(t, st.Meta.Serial+1, after.Meta.Serial)
		require.Equal(t, after.Meta.Serial, receipt.Serial)
		require.Equal(t, "unknown", actions[0].Quiescence)
		cold := stateDeleteTestServer(t, store)
		replay, err := cold.commitSnapshotRestore(ctx, "lab", request)
		require.NoError(t, err)
		require.Equal(t, receipt, replay)
		// A changed diagnosis is new evidence; it may not be replaced by an old snapshot.
		changed := actions[0]
		changed.Status = runtime.ActionUncertain
		changed.ExpectedSerial = after.Meta.Serial
		changed.Error = "later observation"
		require.NoError(t, repo.CommitActionOutcome(ctx, changed, nil))
		_, err = repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "different-restore", 0)
		require.ErrorIs(t, err, persistence.ErrConflict)
		preserved, err := repo.LoadActions(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, "later observation", preserved[0].Error)
	})
}
