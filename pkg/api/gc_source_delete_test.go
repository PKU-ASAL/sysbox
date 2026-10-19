package api

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMetadataDeleteRetiredPublishersDoesNotReenroll(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.cfg.GCPublicationSource = "api"
		files := map[string][]byte{"field.sysbox.hcl": []byte("# retired project")}
		require.NoError(t, s.workspaceService().UpsertProject(ctx, "lab", files))
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		before, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		sources, err := repo.LoadGCSources(ctx, "lab")
		require.NoError(t, err)
		for name, source := range sources {
			require.NoError(t, repo.RetireGCSource(ctx, "lab", name, before.TopologyUID, source.Version))
		}
		require.NoError(t, s.workspaceService().Delete(ctx, "lab", false))
		sources, err = repo.LoadGCSources(ctx, "lab")
		require.NoError(t, err)
		require.Empty(t, sources, "cleanup must not create a new placement publisher")
		after, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.NotEqual(t, before.TopologyUID, after.TopologyUID)
		_, err = repo.BeginGCSource(ctx, "lab", "api-workspace", before.TopologyUID, 0)
		require.Error(t, err, "old topology identity cannot publish")
	})
}
