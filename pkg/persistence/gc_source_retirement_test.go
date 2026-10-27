package persistence

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGCSourceRetirementPreservesReferencesAndFencesPublisher(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	r, err := OpenFilesystem(path)
	require.NoError(t, err)
	defer r.Close()
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.ErrorIs(t, r.RetireGCSource(ctx, "lab", "host", st.TopologyUID, v), ErrConflict)
	m := GCSourceManifest{Domains: map[string][]string{"revision": {"keep"}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, m))
	require.ErrorIs(t, r.RetireGCSource(ctx, "lab", "host", st.TopologyUID, 1), ErrConflict)
	require.NoError(t, r.RetireGCSource(ctx, "lab", "host", st.TopologyUID, 2))
	require.NoError(t, r.RetireGCSource(ctx, "lab", "host", st.TopologyUID, 2))
	cold, err := OpenFilesystem(path)
	require.NoError(t, err)
	defer cold.Close()
	_, err = cold.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 3)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, cold.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, 1, m), ErrConflict)
	require.NoError(t, cold.update(ctx, "lab", func(a *aggregate) error {
		require.Equal(t, GCSourceRetirementFormatVersion, a.Format)
		refs, err := gcReferencesAt(a, time.Now().Add(48*time.Hour))
		require.NoError(t, err)
		require.Equal(t, 1, refs.Domains["revision"].Count)
		return nil
	}))
	require.ErrorIs(t, cold.StateBackend("lab").(*stateBackend).Delete(ctx), ErrConflict, "retirement is not reference deletion")
}
