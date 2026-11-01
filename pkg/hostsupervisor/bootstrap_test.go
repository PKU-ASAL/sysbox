package hostsupervisor

import (
	"github.com/oslab/sysbox/pkg/config"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestHostEnvironmentDoesNotInheritInvokerSettings(t *testing.T) {
	t.Setenv("PATH", "/untrusted")
	t.Setenv("LD_PRELOAD", "/untrusted/lib.so")
	t.Setenv("DOCKER_HOST", "tcp://untrusted:2375")
	t.Setenv("SYSBOX_STATE_BACKEND", "sqlite:///untrusted.db")
	cfg := config.DefaultServiceConfig()
	cfg.HostSupervisor.DockerSocket = "/run/private/docker.sock"
	env, err := HostEnvironment("/etc/sysbox-host/sysbox.yaml", cfg)
	require.NoError(t, err)
	require.Contains(t, env, "PATH="+HostToolPath)
	require.Contains(t, env, "DOCKER_HOST=unix:///run/private/docker.sock")
	for _, entry := range env {
		require.NotContains(t, entry, "untrusted")
	}
	cfg.HostSupervisor.DockerSocket = "relative.sock"
	_, err = HostEnvironment("/etc/config", cfg)
	require.Error(t, err)
}

func TestHostToolChecksOriginalSymlinkAncestors(t *testing.T) {
	root := t.TempDir()
	trusted := filepath.Join(root, "tool")
	require.NoError(t, os.WriteFile(trusted, []byte("#!/bin/sh\n"), 0755))
	links := filepath.Join(root, "links")
	require.NoError(t, os.Mkdir(links, 0755))
	link := filepath.Join(links, "tool")
	require.NoError(t, os.Symlink(trusted, link))
	path, err := trustedHostToolPath(link, 0)
	require.NoError(t, err)
	require.Equal(t, trusted, path)
	require.NoError(t, os.Chmod(links, 0777))
	_, err = trustedHostToolPath(link, 0)
	require.Error(t, err, "a trusted destination cannot bless a mutable path chain")
}
