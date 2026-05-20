package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreflightDetectsMissingLocalRootfs(t *testing.T) {
	dir := t.TempDir()
	workspaces := filepath.Join(dir, "workspaces")
	runs := filepath.Join(dir, "runs")
	topoDir := filepath.Join(workspaces, "missing-rootfs")
	require.NoError(t, os.MkdirAll(topoDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(topoDir, "field.sysbox.hcl"), []byte(`
substrate "firecracker" { alias = "fc" }

resource "sysbox_image" "vm" {
  substrate = substrate.firecracker.fc
  rootfs    = "/no/such/rootfs.ext4"
}
`), 0o644))

	s := NewServer(runs, workspaces)
	res, err := s.preflightTopology("missing-rootfs")
	require.NoError(t, err)
	require.False(t, res.OK)
	require.NotNil(t, res.err())

	var found bool
	for _, c := range res.Checks {
		if c.Name == "image:vm:rootfs" {
			found = true
			require.False(t, c.OK)
			require.Equal(t, "error", c.Severity)
		}
	}
	require.True(t, found)
}

func TestPreflightAllowsURLArtifactWithWarningWithoutSHA(t *testing.T) {
	dir := t.TempDir()
	workspaces := filepath.Join(dir, "workspaces")
	runs := filepath.Join(dir, "runs")
	topoDir := filepath.Join(workspaces, "url-rootfs")
	require.NoError(t, os.MkdirAll(topoDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(topoDir, "field.sysbox.hcl"), []byte(`
substrate "firecracker" { alias = "fc" }

resource "sysbox_image" "vm" {
  substrate = substrate.firecracker.fc
  rootfs    = "https://example.invalid/rootfs.ext4"
}
`), 0o644))

	s := NewServer(runs, workspaces)
	res, err := s.preflightTopology("url-rootfs")
	require.NoError(t, err)

	var found bool
	for _, c := range res.Checks {
		if c.Name == "image:vm:rootfs" {
			found = true
			require.True(t, c.OK)
			require.Equal(t, "warning", c.Severity)
		}
	}
	require.True(t, found)
}
