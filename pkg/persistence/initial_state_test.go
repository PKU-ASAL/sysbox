package persistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestInitialStateMakesEffectFreeDestroyProvable(t *testing.T) {
	for _, kind := range []string{"file", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			var repo *Repository
			var err error
			if kind == "file" {
				repo, err = OpenFilesystem(t.TempDir())
			} else {
				repo, err = OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
			}
			require.NoError(t, err)
			defer repo.Close()
			ctx := context.Background()
			st, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			now := time.Now().UTC()
			run := controlplane.Run{ID: "destroy", Topology: "lab", Op: "destroy", AgentID: "host", Status: controlplane.RunRunning, QueuedAt: now, StartedAt: now, Attempt: 1, LeaseOwner: "lease"}
			_, err = repo.ProveCleanup(ctx, run)
			require.Error(t, err)
			require.NoError(t, repo.InitializeAuthorizedTopology(ctx, "lab", st.TopologyUID))
			after, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.EqualValues(t, 1, after.Meta.Serial)
			require.NoError(t, repo.InitializeAuthorizedTopology(ctx, "lab", st.TopologyUID))
			after, err = repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.EqualValues(t, 1, after.Meta.Serial)
			proof, err := repo.ProveCleanup(ctx, run)
			require.NoError(t, err)
			require.Equal(t, st.TopologyUID, proof.TopologyUID)
			require.ErrorIs(t, repo.InitializeAuthorizedTopology(ctx, "lab", "old-identity"), ErrConflict)
		})
	}
}

func TestInitialStateRejectsLostAggregateIdentity(t *testing.T) {
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	// A vanished record is assigned a new UID by loading; the signed old owner
	// must never be usable to initialize or certify that new record.
	require.ErrorIs(t, repo.InitializeAuthorizedTopology(context.Background(), "lost", "old-owner"), ErrConflict)
}

func TestInitialStateRejectsRetainedGCSource(t *testing.T) {
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	_, err = repo.BeginGCSource(ctx, "lab", "external", st.TopologyUID, 0)
	require.NoError(t, err)
	require.ErrorIs(t, repo.InitializeAuthorizedTopology(ctx, "lab", st.TopologyUID), ErrConflict)
	after, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Zero(t, after.Meta.Serial)
}

func TestInitialStatePreservesCompletedMetadataPublication(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	version, err := repo.BeginGCSource(ctx, "lab", "host-workspace", st.TopologyUID, 0)
	require.NoError(t, err)
	manifest := GCSourceManifest{Domains: map[string][]string{"revision": {"sha256:revision"}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}
	require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host-workspace", st.TopologyUID, version, manifest))
	before, err := repo.LoadGCSources(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, repo.InitializeAuthorizedTopology(ctx, "lab", st.TopologyUID))
	after, err := repo.LoadGCSources(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, before, after)
}
