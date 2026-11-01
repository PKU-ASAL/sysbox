package hostexec

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func saveLocalTestState(t *testing.T, path, topology string, st *state.State) {
	t.Helper()
	repo, err := persistence.OpenConfigured(context.Background(), "", path)
	require.NoError(t, err)
	defer repo.Close()
	mgr := state.NewManagerWithBackend(repo.StateBackend(topology))
	stored, err := mgr.Load()
	require.NoError(t, err)
	st.TopologyUID = stored.TopologyUID
	st.Meta = stored.Meta
	for i := range st.Resources {
		r := &st.Resources[i]
		r.OwnerUID = st.TopologyUID
		r.ResourceUID = st.TopologyUID + "/" + r.Address.String()
		r.Generation = 1
	}
	require.NoError(t, mgr.Save(st))
}
