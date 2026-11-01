package persistence

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestAggregateSnapshotRestorePreservesIdentityAndAdvancesSerial(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			ctx := context.Background()
			m := state.NewManagerWithBackend(r.StateBackend("lab"))
			st, err := m.Load()
			require.NoError(t, err)
			st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "node"), OwnerUID: st.TopologyUID, ResourceUID: "node-uid", Generation: 2, ExternalID: "external", Attributes: state.MustAttributes(map[string]any{"value": "before"})}}
			require.NoError(t, m.Save(st))
			snapshots, ok := m.Backend().(state.SnapshotBackend)
			require.True(t, ok, "canonical aggregate must support snapshot lifecycle")
			snap, err := snapshots.Snapshot(ctx, "before update")
			require.NoError(t, err)
			current, err := m.Load()
			require.NoError(t, err)
			current.Resources[0].Attributes = state.MustAttributes(map[string]any{"value": "after"})
			require.NoError(t, m.Save(current))
			require.ErrorIs(t, snapshots.RestoreSnapshot(ctx, snap.ID), ErrConflict, "historical public fields may contain effect targets; do not blindly rewind them")
			current, err = m.Load()
			require.NoError(t, err)
			current.Resources[0].Attributes = state.MustAttributes(map[string]any{"value": "before"})
			require.NoError(t, m.Save(current))
			before, err := m.Load()
			require.NoError(t, err)
			require.NoError(t, snapshots.RestoreSnapshot(ctx, snap.ID))
			restored, err := m.Load()
			require.NoError(t, err)
			require.Greater(t, restored.Meta.Serial, before.Meta.Serial)
			require.Equal(t, "before", restored.Resources[0].Str("value"))
			require.Equal(t, int64(2), restored.Resources[0].Generation)
			require.Equal(t, st.TopologyUID, restored.TopologyUID)
			require.Equal(t, state.ResourceUnknown, restored.Resources[0].Status)
			all, err := snapshots.ListSnapshots(ctx)
			require.NoError(t, err)
			require.Len(t, all, 1)
			restored.Resources[0].Generation = 3
			require.NoError(t, m.Save(restored))
			require.Error(t, snapshots.RestoreSnapshot(ctx, snap.ID), "snapshot cannot roll back a resource incarnation")
		})
	}
}

func TestSnapshotRejectsCorruptionUnresolvedWorkAndDifferentLineage(t *testing.T) {
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	backend := r.StateBackend("lab").(*stateBackend)
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	snap, err := backend.Snapshot(ctx, "empty")
	require.NoError(t, err)
	action := runtime.ActionRecord{ID: "pending", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "uid", Generation: 1}
	require.NoError(t, r.CommitActionIntent(ctx, action))
	require.ErrorIs(t, backend.RestoreSnapshot(ctx, snap.ID), ErrConflict)
	require.NoError(t, r.update(ctx, "lab", func(a *aggregate) error {
		value := a.Snapshots[snap.ID]
		value.Digest = "bad"
		a.Snapshots[snap.ID] = value
		return nil
	}))
	require.ErrorIs(t, backend.RestoreSnapshot(ctx, snap.ID), ErrCorruptRecord)
	require.ErrorIs(t, backend.DeleteSnapshot(ctx, "unknown"), ErrNotFound)
}

func TestSnapshotRestoreRejectsChangedInterpretation(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		for _, field := range []string{"lineage", "schema", "resource_type"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				ctx := context.Background()
				repo := resetTestRepository(t, kind)
				manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
				current, err := manager.Load()
				require.NoError(t, err)
				current.Lineage = "original"
				current.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", SchemaVersion: 1, Driver: "docker", OwnerUID: current.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: "container"}}
				require.NoError(t, manager.Save(current))
				snapshot, err := manager.Snapshot(ctx, "before")
				require.NoError(t, err)
				current, err = manager.Load()
				require.NoError(t, err)
				switch field {
				case "lineage":
					current.Lineage = "different"
				case "schema":
					current.Resources[0].SchemaVersion++
				case "resource_type":
					current.Resources[0].ResourceType = "other"
				}
				require.NoError(t, manager.Save(current))
				before, err := manager.Load()
				require.NoError(t, err)
				err = manager.Backend().(state.SnapshotBackend).RestoreSnapshot(ctx, snapshot.ID)
				require.ErrorIs(t, err, ErrConflict)
				after, err := manager.Load()
				require.NoError(t, err)
				require.Equal(t, before.Lineage, after.Lineage)
				require.Equal(t, before.Meta.Serial, after.Meta.Serial)
				require.Equal(t, before.Resources, after.Resources)
			})
		}
	}
}
