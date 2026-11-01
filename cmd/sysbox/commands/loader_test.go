package commands

import (
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestCLIAndExecutionShareConfiguredRepository(t *testing.T) {
	oldBackend, oldState := flagBackend, flagStateFile
	t.Cleanup(func() { flagBackend, flagStateFile = oldBackend, oldState })
	for _, tc := range []struct{ name, backend, topology string }{
		{"placeholder", "{topology}.db", "lab"},
		{"explicit_topology", "state.db?topology=selected", "selected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			flagStateFile = filepath.Join(root, "lab", "state.json")
			flagBackend = "sqlite://" + filepath.Join(root, tc.backend)
			require.Equal(t, tc.topology, localTopology())
			mgr, err := newManager()
			require.NoError(t, err)
			first, err := mgr.Load()
			require.NoError(t, err)
			pool := &controlstore.LifecycleRepositories{}
			defer pool.Close()
			repo, err := pool.Open(root, flagBackend, localTopology())
			require.NoError(t, err)
			other := state.NewManagerWithBackend(repo.StateBackend(localTopology()))
			require.NoError(t, err)
			second, err := other.Load()
			require.NoError(t, err)
			require.Equal(t, first.TopologyUID, second.TopologyUID)
		})
	}
}
