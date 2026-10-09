package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRunSaveCannotBypassMaintenanceBarrier(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
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
