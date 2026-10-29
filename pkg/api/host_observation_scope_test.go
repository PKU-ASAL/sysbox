package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestHostObservationScopeUsesCanonicalPlacementForLocalAndSQL(t *testing.T) {
	for _, backend := range []string{"local", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			cfg := config.DefaultServiceConfig()
			cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir = t.TempDir(), t.TempDir()
			if backend == "sqlite" {
				cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "canonical.db")
			}
			b, err := NewExecutionBridge(cfg)
			require.NoError(t, err)
			defer b.Close()
			key, _, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			require.NoError(t, b.BindHostIdentity("owner", key))
			for _, id := range []string{"owner", "foreign"} {
				repo, err := b.ActionRepository(id)
				require.NoError(t, err)
				st, err := repo.LoadState(context.Background(), id)
				require.NoError(t, err)
				manager, err := b.StateManager(id)
				require.NoError(t, err)
				require.NoError(t, manager.Save(st))
				require.NoError(t, b.workspaces.bindPlacement(id, id, controlplane.AgentProtocolVersion))
			}
			names, err := b.ObservationTopologies(context.Background())
			require.NoError(t, err)
			require.Equal(t, []string{"owner"}, names)
		})
	}
}
