package main

import (
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitHostConfigurationAlsoControlsProviderPreflight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nproviders:\n  firecracker:\n    binary: /explicit/firecracker\n    jailer: /explicit/jailer\n    jail_root: /explicit/jails\n    cgroup_parent: explicit-vms\n    workdir: /explicit/workdir\nagent:\n  execution_grant_public_key_file: /explicit/issuer.pub\n"), 0600))
	t.Setenv("SYSBOX_CONFIG", "/missing-default-config")
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, initializeProviders(path))
	cfg, err := config.LoadServiceConfig("")
	require.NoError(t, err)
	require.Equal(t, "/explicit/jailer", cfg.Providers.Firecracker.Jailer)
	require.Equal(t, "/explicit/issuer.pub", cfg.Agent.ExecutionGrantPublicKeyFile)
	provider, err := driver.DefaultRegistry.RequireNode("firecracker")
	require.NoError(t, err)
	var jailerFound bool
	for _, check := range provider.PreflightChecks(true) {
		if check.Name == "jailer_trust" {
			jailerFound = true
			require.Contains(t, check.Message, "/explicit/jailer")
		}
	}
	require.True(t, jailerFound)
}
