package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestSnapshotRetentionPlanPreservesEvidenceAndRejectsChangedCatalog(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		snapshots := repo.StateBackend("lab").(state.SnapshotBackend)
		for i := 0; i < 3; i++ {
			_, err = snapshots.Snapshot(ctx, "historical")
			require.NoError(t, err)
		}
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.NoError(t, repo.CommitActionIntent(ctx, runtime.ActionRecord{ID: "pending", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}))
		unresolved, err := snapshots.Snapshot(ctx, "unresolved")
		require.NoError(t, err)
		cutoff := time.Now().UTC()
		policy := persistence.SnapshotRetentionPolicy{KeepLast: 1, OlderThan: cutoff, Limit: 2}
		got := remoteRecoveryRequest(t, s, "POST", "/v1/topologies/lab/state/snapshots/retention-plan", policy)
		require.Equal(t, 200, got.Code, got.Body.String())
		var plan persistence.SnapshotRetentionPlan
		require.NoError(t, json.Unmarshal(got.Body.Bytes(), &plan))
		require.Len(t, plan.DeleteIDs, 2)
		require.NotContains(t, plan.DeleteIDs, unresolved.ID)
		stale := plan
		_, err = snapshots.Snapshot(ctx, "newer")
		require.NoError(t, err)
		rejected := remoteRecoveryRequest(t, s, "POST", "/v1/topologies/lab/state/snapshots/retention-apply", stale)
		require.Equal(t, 409, rejected.Code, rejected.Body.String())
		got = remoteRecoveryRequest(t, s, "POST", "/v1/topologies/lab/state/snapshots/retention-plan", policy)
		require.Equal(t, 200, got.Code, got.Body.String())
		require.NoError(t, json.Unmarshal(got.Body.Bytes(), &plan))
		tampered := plan
		tampered.DeleteIDs = []string{unresolved.ID}
		require.Equal(t, 409, remoteRecoveryRequest(t, s, "POST", "/v1/topologies/lab/state/snapshots/retention-apply", tampered).Code)
		before, actions, err := repo.LoadStateAndActions(ctx, "lab")
		require.NoError(t, err)
		applied := remoteRecoveryRequest(t, s, "POST", "/v1/topologies/lab/state/snapshots/retention-apply", plan)
		require.Equal(t, 200, applied.Code, applied.Body.String())
		remaining, err := snapshots.ListSnapshots(ctx)
		require.NoError(t, err)
		require.Len(t, remaining, 3)
		found := false
		for _, entry := range remaining {
			if entry.ID == unresolved.ID {
				found = true
			}
		}
		require.True(t, found)
		after, current, err := repo.LoadStateAndActions(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, before, after)
		require.Equal(t, actions, current)
		require.Equal(t, 409, remoteRecoveryRequest(t, s, "POST", "/v1/topologies/lab/state/snapshots/retention-apply", plan).Code, "used catalog plan cannot prune a successor")
	})
}
