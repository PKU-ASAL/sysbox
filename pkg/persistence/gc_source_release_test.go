package persistence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQuiescentDeleteReleasesOnlyFullyRetiredSourceDomains(t *testing.T) {
	for _, scenario := range []string{"ready", "completed-admission", "active-source", "resource-reference", "snapshot", "raw-delete"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			r, err := OpenFilesystem(t.TempDir())
			require.NoError(t, err)
			defer r.Close()
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
			require.NoError(t, err)
			m := GCSourceManifest{Domains: map[string][]string{"revision": {"old-revision"}, "artifact": {"old-artifact"}, "cache": {}, "placement": {"host"}}, ValidUntil: time.Now().Add(time.Hour)}
			if scenario == "resource-reference" {
				m.References = []GCResourceReference{{Source: "external", ResourceType: "sysbox_node", Driver: "docker", ExternalID: "still-protected", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}}
			}
			require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, m))
			if scenario != "active-source" {
				require.NoError(t, r.RetireGCSource(ctx, "lab", "host", st.TopologyUID, 2))
			}
			if scenario == "snapshot" {
				_, err = r.StateBackend("lab").(*stateBackend).Snapshot(ctx, "keep")
				require.NoError(t, err)
			}
			if scenario == "raw-delete" {
				require.ErrorIs(t, r.StateBackend("lab").(*stateBackend).Delete(ctx), ErrConflict)
				return
			}
			var next []byte
			err = r.update(ctx, "lab", func(a *aggregate) error {
				if scenario == "completed-admission" {

					a.Format = FormatVersion
				}
				raw, err := json.Marshal(a)
				if err != nil {
					return err
				}
				next, err = ApplyQuiescentLifecycleDelete(raw, nil, "lab", a.Serial, st.TopologyUID)
				return err
			})
			if scenario != "ready" && scenario != "completed-admission" {
				require.ErrorIs(t, err, ErrConflict)
				return
			}
			require.NoError(t, err)
			a, err := decodeAggregate(next)
			require.NoError(t, err)
			require.Empty(t, a.GCSources)
			require.NotEqual(t, st.TopologyUID, a.State.TopologyUID)
			require.Equal(t, FormatVersion, a.Format)
		})
	}
}

func TestMetadataDeleteReceiptSurvivesLostCommitResponse(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r := resetTestRepository(t, kind)
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.NoError(t, r.DeleteQuiescentLifecycle(ctx, "lab", st.Meta.Serial, st.TopologyUID, nil))
			after, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.NoError(t, r.DeleteQuiescentLifecycle(ctx, "lab", st.Meta.Serial, st.TopologyUID, nil), "lost commit response must not strand filesystem cleanup")
			again, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, after.TopologyUID, again.TopologyUID)
			_, err = r.BeginGCSource(ctx, "lab", "new", after.TopologyUID, 0)
			require.NoError(t, err)
			require.ErrorIs(t, r.DeleteQuiescentLifecycle(ctx, "lab", st.Meta.Serial, st.TopologyUID, nil), ErrConflict, "old cleanup may not remove a reused topology")
		})
	}
}

func TestUnrelatedResetClearsPriorDeletionReceipt(t *testing.T) {
	a := newAggregate()
	old := a.State.TopologyUID
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	next, err := ApplyQuiescentLifecycleDelete(raw, nil, "lab", 0, old)
	require.NoError(t, err)
	a, err = decodeAggregate(next)
	require.NoError(t, err)
	require.NoError(t, resetAggregate(a))
	raw, err = json.Marshal(a)
	require.NoError(t, err)
	_, err = ApplyQuiescentLifecycleDelete(raw, nil, "lab", 0, old)
	require.ErrorIs(t, err, ErrConflict)
}
