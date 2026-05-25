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
version: 1
api:
  listen: ":9999"
  console:
    default_timeout: 30m
    max_timeout: 2h
    allowed_roles:
      - console
  rbac:
    admin_roles:
      - admin
      - platform
agent:
  policy:
    allowed_workspaces:
      - lab
    allowed_substrates:
      - docker
    allowed_commands:
      - run_assigned
      - node_operation
    allow_console: false
    allow_import: false
paths:
  home: /srv/sysbox
  cache: /srv/cache
state:
  backend: "postgres://example/sysbox?topology={topology}"
supervisor:
  policy: restart_on_crash
  interval: 5s
providers:
  default_policy:
    preflight: error
  firecracker:
    binary: /opt/fc/firecracker
    kernel: /opt/fc/vmlinux
artifacts:
  registries:
    - name: local
      uri: file:///opt/sysbox/artifacts
  policy:
    cache_mode: readonly
    verify: strict
`), 0o644))

	cfg, err := LoadServiceConfig(path)
	require.NoError(t, err)
	require.Equal(t, ":9999", cfg.API.Listen)
	require.Equal(t, "30m", cfg.API.Console.DefaultTimeout)
	require.Equal(t, "2h", cfg.API.Console.MaxTimeout)
	require.Equal(t, []string{"console"}, cfg.API.Console.AllowedRoles)
	require.Equal(t, []string{"admin", "platform"}, cfg.API.RBAC.AdminRoles)
	require.Equal(t, []string{"lab"}, cfg.Agent.Policy.AllowedWorkspaces)
	require.Equal(t, []string{"docker"}, cfg.Agent.Policy.AllowedSubstrates)
	require.Equal(t, []string{"run_assigned", "node_operation"}, cfg.Agent.Policy.AllowedCommands)
	require.False(t, *cfg.Agent.Policy.AllowConsole)
	require.False(t, *cfg.Agent.Policy.AllowImport)
	require.Equal(t, "/srv/sysbox/workspaces", cfg.Paths.WorkspacesDir)
	require.Equal(t, "/srv/sysbox/runs", cfg.Paths.RunsDir)
	require.Equal(t, "/srv/sysbox/firecracker", cfg.Providers.Firecracker.Workdir)
	require.Equal(t, "restart_on_crash", cfg.Supervisor.Policy)
	require.Equal(t, "/opt/fc/firecracker", cfg.Providers.Firecracker.Binary)
	require.Equal(t, "error", cfg.Providers.DefaultPolicy.Preflight)
	require.Equal(t, "readonly", cfg.Artifacts.Policy.CacheMode)
	require.Equal(t, "strict", cfg.Artifacts.Policy.Verify)
	require.Len(t, cfg.Artifacts.Registries, 1)
}

func TestLoadServiceConfigEnvOverridesAreCanonicalOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysbox.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
version: 1
api:
  listen: ":9999"
state:
  backend: "local"
providers:
  firecracker:
    binary: /from/yaml/firecracker
`), 0o644))
	t.Setenv("SYSBOX_STATE_BACKEND", "postgres://override/sysbox?topology={topology}")
	t.Setenv("SYSBOX_PROVIDER_FIRECRACKER_BIN", "/from/provider/env/firecracker")

	cfg, err := LoadServiceConfig(path)
	require.NoError(t, err)
	require.Equal(t, ":9999", cfg.API.Listen)
	require.Equal(t, DefaultHomeDir, cfg.Paths.Home)
	require.Equal(t, "postgres://override/sysbox?topology={topology}", cfg.State.Backend)
	require.Equal(t, "/from/provider/env/firecracker", cfg.Providers.Firecracker.Binary)
}

func TestLoadServiceConfigProviderEnvOverridesYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysbox.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
version: 1
providers:
  firecracker:
    binary: /from/yaml/firecracker
`), 0o644))
	t.Setenv("SYSBOX_PROVIDER_FIRECRACKER_BIN", "/provider/firecracker")

	cfg, err := LoadServiceConfig(path)
	require.NoError(t, err)
	require.Equal(t, "/provider/firecracker", cfg.Providers.Firecracker.Binary)
}

func TestLoadServiceConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysbox.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
version: 1
unknown: true
`), 0o644))

	_, err := LoadServiceConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "field unknown not found")
}

func TestLoadServiceConfigRejectsInvalidValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysbox.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
version: 1
supervisor:
  policy: yolo
`), 0o644))

	_, err := LoadServiceConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "supervisor.policy")
}
