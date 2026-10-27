package api

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestLifecycleCatalogUsesDatabaseWithoutFakeTopology(t *testing.T) {
	for name, selector := range map[string]string{"fixed": "lab", "template": "{topology}", "unbound": ""} {
		t.Run(name, func(t *testing.T) {
			cfg := config.MustLoadServiceConfig("")
			cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir = t.TempDir(), t.TempDir()
			cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "state.db")
			if selector != "" {
				cfg.State.Backend += "?topology=" + selector
			}
			s := NewServerWithConfig(cfg)
			mgr, err := s.stateManager("lab")
			require.NoError(t, err)
			st, err := mgr.Load()
			require.NoError(t, err)
			require.NoError(t, mgr.Save(st))
			listed, err := s.workspaceService().List(context.Background())
			require.NoError(t, err)
			require.Len(t, listed, 1)
			require.Equal(t, "lab", listed[0].Name)
			require.True(t, listed[0].HasState)
		})
	}
}

func TestSQLiteMetadataUsesDecodedDatabaseLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state #1.db")
	u := url.URL{Scheme: "sqlite", Path: path, RawQuery: "topology=lab"}
	s, err := newAPIStore(t.TempDir(), u.String())
	require.NoError(t, err)
	require.Equal(t, path, s.(*sqliteAPIStore).dbPath)
}

func TestLifecycleCatalogIncludesUnresolvedFirstCreate(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, repo.CommitActionIntent(ctx, runtime.ActionRecord{ID: "pending", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "web-uid", Generation: 1}))
	listed, err := s.workspaceService().List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "lab", listed[0].Name)
	require.True(t, listed[0].HasState)
	loaded, err := s.workspaceService().LoadState("lab")
	require.NoError(t, err, "a listed topology with a pending action must not disappear from the state API")
	require.Equal(t, st.TopologyUID, loaded.TopologyUID)
}
