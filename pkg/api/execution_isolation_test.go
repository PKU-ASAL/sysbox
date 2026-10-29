package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
)

func TestHostExecutionDoesNotOpenControlPlaneSigningKey(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	// A host must not need, or attempt to open, the API issuer's private key.
	cfg.API.ExecutionGrant.SigningKeyFile = filepath.Join(t.TempDir(), "api-only-private-key")
	bridge, err := NewExecutionBridge(cfg)
	require.NoError(t, err)
	require.NotNil(t, bridge)
	t.Cleanup(func() { require.NoError(t, bridge.Close()) })
}

func TestHostExecutionDoesNotInitializeConsoleRegistry(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	path := filepath.Join(cfg.Paths.RunsDir, "lab", "sessions", "broken.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	raw := []byte("{broken-console-record")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	bridge, err := NewExecutionBridge(cfg)
	require.NoError(t, err, "console startup belongs to API; host only opens execution stores")
	require.NotNil(t, bridge)
	t.Cleanup(func() { require.NoError(t, bridge.Close()) })
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, raw, after)
}
