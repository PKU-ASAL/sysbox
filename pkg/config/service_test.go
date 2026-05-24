package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadServiceConfigFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysbox.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
api:
  listen: ":9999"
paths:
  home: /srv/sysbox
  cache: /srv/cache
state:
  backend: "postgres://example/sysbox?topology={topology}"
supervisor:
  policy: restart_on_crash
  interval: 5s
providers:
  firecracker:
    binary: /opt/fc/firecracker
    kernel: /opt/fc/vmlinux
`), 0o644))

	cfg, err := LoadServiceConfig(path)
	require.NoError(t, err)
	require.Equal(t, ":9999", cfg.API.Listen)
	require.Equal(t, "/srv/sysbox/workspaces", cfg.Paths.WorkspacesDir)
	require.Equal(t, "/srv/sysbox/runs", cfg.Paths.RunsDir)
	require.Equal(t, "/srv/sysbox/firecracker", cfg.Providers.Firecracker.Workdir)
	require.Equal(t, "restart_on_crash", cfg.Supervisor.Policy)
	require.Equal(t, "/opt/fc/firecracker", cfg.Providers.Firecracker.Binary)
}

func TestLoadServiceConfigEnvOverridesYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysbox.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
api:
  listen: ":9999"
state:
  backend: "local"
providers:
  firecracker:
    binary: /from/yaml/firecracker
`), 0o644))
	t.Setenv("SYSBOX_API_LISTEN", ":7777")
	t.Setenv("SYSBOX_STATE_BACKEND", "postgres://override/sysbox?topology={topology}")
	t.Setenv("SYSBOX_FIRECRACKER_BIN", "/from/env/firecracker")

	cfg, err := LoadServiceConfig(path)
	require.NoError(t, err)
	require.Equal(t, ":7777", cfg.API.Listen)
	require.Equal(t, "postgres://override/sysbox?topology={topology}", cfg.State.Backend)
	require.Equal(t, "/from/env/firecracker", cfg.Providers.Firecracker.Binary)
}
