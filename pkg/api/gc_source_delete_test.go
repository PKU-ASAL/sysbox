package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/workspace"
	"github.com/stretchr/testify/require"
)

func TestMetadataDeleteRetiredPublishersDoesNotReenroll(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
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

func TestMetadataDeletionCleanupResumesAfterCommittedReset(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.cfg.GCPublicationSource = "api"
		require.NoError(t, s.workspaceService().UpsertProject(ctx, "lab", map[string][]byte{"field.sysbox.hcl": []byte("# delete me")}))
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		sources, err := repo.LoadGCSources(ctx, "lab")
		require.NoError(t, err)
		for name, source := range sources {
			require.NoError(t, repo.RetireGCSource(ctx, "lab", name, st.TopologyUID, source.Version))
		}
		w := s.workspaceService()
		require.NoError(t, w.WriteDeletionIntent(workspace.DeletionIntent{Topology: "lab", UID: st.TopologyUID, Serial: st.Meta.Serial, Created: time.Now()}))
		require.NoError(t, s.deleteStateWithAuthority(ctx, "lab", st.Meta.Serial, st.TopologyUID))
		// New service instance has no cached deletion state. Recover only the same
		// committed deletion, then finish local workspace/placement cleanup.
		cold := *w
		require.NoError(t, cold.RecoverDeletionIntents(ctx))
		require.NoDirExists(t, filepath.Join(w.WorkspacesDir, "lab"))
		require.NoFileExists(t, w.DeletionIntentPath("lab"))
	})
}
