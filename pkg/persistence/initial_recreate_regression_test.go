package persistence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestDeletedTopologyCanStartNewIncarnation(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, repo.InitializeAuthorizedTopology(ctx, "lab", st.TopologyUID))
	require.NoError(t, repo.DeleteQuiescentLifecycle(ctx, "lab", 1, st.TopologyUID, nil))
	next, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NotEqual(t, st.TopologyUID, next.TopologyUID)
	require.NoError(t, repo.InitializeAuthorizedTopology(ctx, "lab", next.TopologyUID))
}

func TestRecreateRejectsUnprovenEmptyDeletion(t *testing.T) {
	for _, scenario := range []string{"serial-zero", "legacy", "retained-resource"} {
		t.Run(scenario, func(t *testing.T) {
			a := newAggregate()
			if scenario != "serial-zero" {
				a.Serial = 1
			}
			if scenario == "retained-resource" {
				a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), OwnerUID: a.State.TopologyUID, ResourceUID: "resource", Generation: 1, ExternalID: "container"}}
			}
			raw, err := json.Marshal(a)
			require.NoError(t, err)
			next, err := ApplyQuiescentLifecycleDelete(raw, nil, "lab", a.Serial, a.State.TopologyUID)
			require.NoError(t, err)
			reset, err := decodeAggregate(next)
			require.NoError(t, err)
			if scenario == "legacy" {
				reset.DeletedFrom.DeletedAt = time.Time{}
				reset.DeletedFrom.Empty = false
				next, err = json.Marshal(reset)
				require.NoError(t, err)
			}
			_, err = ApplyInitialTopology(next, "lab", reset.State.TopologyUID)
			require.ErrorIs(t, err, ErrConflict)
		})
	}
}

func TestDeletionReceiptNewFieldsRejectCorruptCombinations(t *testing.T) {
	a := newAggregate()
	a.DeletedFrom = &LifecycleDeletionReceipt{TopologyUID: "previous", Serial: 1}
	require.NoError(t, validateAggregate(a, "lab"), "legacy receipt remains readable")
	a.DeletedFrom.Empty = true
	require.ErrorIs(t, validateAggregate(a, "lab"), ErrCorruptRecord)
	a.DeletedFrom.DeletedAt = time.Now().UTC()
	a.UpdatedAt = a.DeletedFrom.DeletedAt
	require.NoError(t, validateAggregate(a, "lab"))
	a.DeletedFrom.Serial = 0
	require.ErrorIs(t, validateAggregate(a, "lab"), ErrCorruptRecord)
	a.DeletedFrom.Serial = 1
	a.DeletedFrom.DeletedAt = a.UpdatedAt.Add(time.Second)
	require.ErrorIs(t, validateAggregate(a, "lab"), ErrCorruptRecord)
}
