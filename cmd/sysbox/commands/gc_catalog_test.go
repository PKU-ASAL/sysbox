package commands

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestGCCatalogCollectsAllTopologiesAndResourceUIDs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := persistence.OpenSQLite(path)
	require.NoError(t, err)
	defer repo.Close()
	for _, name := range []string{"one", "two"} {
		m := state.NewManagerWithBackend(repo.StateBackend(name))
		st, err := m.Load()
		require.NoError(t, err)
		st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "vm"), Driver: "docker", ExternalID: name, ResourceUID: name + "-uid", OwnerUID: st.TopologyUID, Generation: 1}}
		require.NoError(t, m.Save(st))
	}
	active, err := collectCatalogIDs(ctx, "sqlite://"+path)
	require.NoError(t, err)
	require.Len(t, active.owners, 2)
	require.True(t, active.containers["one"])
	require.True(t, active.containers["two"])
	require.Len(t, active.resourceUIDs, 2)
}
