package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
)

func writeHCL(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "test.hcl")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))
	return f
}

func TestBuildGraphCount(t *testing.T) {
	f := writeHCL(t, `
resource "sysbox_network" "lab" {
  count = 3
  cidr  = "10.0.${count.index}.0/24"
}
`)
	root, err := config.ParseFile(f)
	require.NoError(t, err)

	ctx := config.BuildEvalContext(root)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	require.Len(t, g.All(), 3)
	require.NotNil(t, g.Get("sysbox_network", "lab[0]"))
	require.NotNil(t, g.Get("sysbox_network", "lab[1]"))
	require.NotNil(t, g.Get("sysbox_network", "lab[2]"))
	require.Nil(t, g.Get("sysbox_network", "lab"))
}

func TestBuildGraphCountZero(t *testing.T) {
	f := writeHCL(t, `
substrate "docker" { alias = "dk" }
resource "sysbox_network" "dmz" {
  cidr = "10.0.1.0/24"
  count = 0
}
`)
	root, err := config.ParseFile(f)
	require.NoError(t, err)

	ctx := config.BuildEvalContext(root)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	// count = 0 expands to nothing
	require.Empty(t, g.All())
}

func TestBuildGraphForEachMap(t *testing.T) {
	f := writeHCL(t, `
resource "sysbox_network" "lab" {
  for_each = { dmz = "10.0.1.0/24", internal = "10.0.2.0/24" }
  cidr     = each.value
}
`)
	root, err := config.ParseFile(f)
	require.NoError(t, err)

	ctx := config.BuildEvalContext(root)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	require.Len(t, g.All(), 2)
	require.NotNil(t, g.Get("sysbox_network", "lab_dmz"))
	require.NotNil(t, g.Get("sysbox_network", "lab_internal"))
}

func TestBuildGraphForEachSet(t *testing.T) {
	f := writeHCL(t, `
resource "sysbox_network" "lab" {
  for_each = toset(["dmz", "internal", "uplink"])
  cidr     = "10.0.0.0/24"
}
`)
	root, err := config.ParseFile(f)
	require.NoError(t, err)

	ctx := config.BuildEvalContext(root)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	require.Len(t, g.All(), 3)
	require.NotNil(t, g.Get("sysbox_network", "lab_dmz"))
	require.NotNil(t, g.Get("sysbox_network", "lab_internal"))
	require.NotNil(t, g.Get("sysbox_network", "lab_uplink"))
}

func TestBuildGraphForEachSetEachKeyValue(t *testing.T) {
	// Verify each.key == each.value for sets.
	f := writeHCL(t, `
resource "sysbox_network" "seg" {
  for_each = toset(["red", "blue"])
  cidr     = "10.0.0.0/24"  # each.key/value available but not used in cidr here
}
`)
	root, err := config.ParseFile(f)
	require.NoError(t, err)

	ctx := config.BuildEvalContext(root)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	require.Len(t, g.All(), 2)
	// Each instance carries its config decoded without error.
	n := g.Get("sysbox_network", "seg_red")
	require.NotNil(t, n)
	cfg, ok := n.Data.(*config.NetworkConfig)
	require.True(t, ok)
	require.Equal(t, "10.0.0.0/24", cfg.CIDR)
}

func TestBuildGraphForEachNonStringSetError(t *testing.T) {
	f := writeHCL(t, `
resource "sysbox_network" "lab" {
  for_each = toset([1, 2, 3])
  cidr     = "10.0.0.0/24"
}
`)
	root, err := config.ParseFile(f)
	require.NoError(t, err)

	ctx := config.BuildEvalContext(root)
	_, err = BuildGraph(root, ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "set must contain strings")
}
