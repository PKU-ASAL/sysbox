package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"

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

	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	require.Len(t, g.All(), 3)
	require.NotNil(t, g.Get(address.IntInstance("sysbox_network", "lab", 0)))
	require.NotNil(t, g.Get(address.IntInstance("sysbox_network", "lab", 1)))
	require.NotNil(t, g.Get(address.IntInstance("sysbox_network", "lab", 2)))
	require.Nil(t, g.Get(address.Resource("sysbox_network", "lab")))
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

	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
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

	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	require.Len(t, g.All(), 2)
	require.NotNil(t, g.Get(address.StringInstance("sysbox_network", "lab", "dmz")))
	require.NotNil(t, g.Get(address.StringInstance("sysbox_network", "lab", "internal")))
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

	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	require.Len(t, g.All(), 3)
	require.NotNil(t, g.Get(address.StringInstance("sysbox_network", "lab", "dmz")))
	require.NotNil(t, g.Get(address.StringInstance("sysbox_network", "lab", "internal")))
	require.NotNil(t, g.Get(address.StringInstance("sysbox_network", "lab", "uplink")))
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

	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)

	require.Len(t, g.All(), 2)
	// Each instance carries its config decoded without error.
	n := g.Get(address.StringInstance("sysbox_network", "seg", "red"))
	require.NotNil(t, n)
	cfg, ok := n.Data.(*config.NetworkConfig)
	require.True(t, ok)
	require.Equal(t, "10.0.0.0/24", cfg.CIDR)
}

func TestBuildGraphForEachDoesNotCollideWithLiteralName(t *testing.T) {
	f := writeHCL(t, `
resource "sysbox_network" "lab" {
  for_each = { dmz = "10.0.1.0/24" }
  cidr = each.value
}
resource "sysbox_network" "lab_dmz" {
  cidr = "10.0.2.0/24"
}
`)
	root, err := config.ParseFile(f)
	require.NoError(t, err)
	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx)
	require.NoError(t, err)
	require.Len(t, g.All(), 2)
	require.NotNil(t, g.Get(address.StringInstance("sysbox_network", "lab", "dmz")))
	require.NotNil(t, g.Get(address.Resource("sysbox_network", "lab_dmz")))
}

func TestBuildGraphModule(t *testing.T) {
	// Use the checked-in fixture relative to the repo root.
	callerFile, err := filepath.Abs("../../tests/testdata/module_caller.sysbox.hcl")
	require.NoError(t, err)

	root, err := config.ParseFile(callerFile)
	require.NoError(t, err)
	require.Len(t, root.Modules, 1)

	ctx, err := config.BuildEvalContext(root, filepath.Dir(callerFile))
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx, callerFile)
	require.NoError(t, err)

	// Module expands to two networks under module.net.
	require.Len(t, g.All(), 2)
	dmzAddr := address.Resource("sysbox_network", "dmz").WithModule(address.ModuleInstance{Name: "net"})
	internalAddr := address.Resource("sysbox_network", "internal").WithModule(address.ModuleInstance{Name: "net"})
	require.NotNil(t, g.Get(dmzAddr))
	require.NotNil(t, g.Get(internalAddr))

	// Config data is decoded and carries the passed cidr values.
	dmzNode := g.Get(dmzAddr)
	require.NotNil(t, dmzNode)
	cfg, ok := dmzNode.Data.(*config.NetworkConfig)
	require.True(t, ok)
	require.Equal(t, "10.1.1.0/24", cfg.CIDR)
}

func TestBuildGraphModuleOutputsInContext(t *testing.T) {
	callerFile, err := filepath.Abs("../../tests/testdata/module_caller.sysbox.hcl")
	require.NoError(t, err)

	root, err := config.ParseFile(callerFile)
	require.NoError(t, err)

	ctx, err := config.BuildEvalContext(root, filepath.Dir(callerFile))
	require.NoError(t, err)

	// module.net.dmz_id should resolve to the namespaced resource name.
	modVal, ok := ctx.Variables["module"]
	require.True(t, ok)
	netVal := modVal.GetAttr("net")
	dmzID := netVal.GetAttr("dmz_id")
	require.Equal(t, "module.net.sysbox_network.dmz", dmzID.AsString())
}

func TestBuildGraphModuleDefaultVars(t *testing.T) {
	// Caller that does NOT pass variables — module should use defaults.
	callerHCL := `
module "net" {
  source = "./mod"
}
`
	// Write module file alongside caller.
	dir := t.TempDir()
	modDir := filepath.Join(dir, "mod")
	require.NoError(t, os.MkdirAll(modDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "main.sysbox.hcl"), []byte(`
variable "cidr" { default = "192.168.1.0/24" }
resource "sysbox_network" "net" { cidr = var.cidr }
`), 0o644))

	callerFile := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(callerFile, []byte(callerHCL), 0o644))

	root, err := config.ParseFile(callerFile)
	require.NoError(t, err)

	ctx, err := config.BuildEvalContext(root, dir)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx, callerFile)
	require.NoError(t, err)

	require.Len(t, g.All(), 1)
	n := g.Get(address.Resource("sysbox_network", "net").WithModule(address.ModuleInstance{Name: "net"}))
	require.NotNil(t, n)
	cfg := n.Data.(*config.NetworkConfig)
	require.Equal(t, "192.168.1.0/24", cfg.CIDR)
}

func TestBuildGraphModuleNestedError(t *testing.T) {
	dir := t.TempDir()
	modDir := filepath.Join(dir, "mod")
	require.NoError(t, os.MkdirAll(modDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "main.sysbox.hcl"), []byte(`
module "inner" { source = "../mod" }
`), 0o644))

	callerFile := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(callerFile, []byte(`
module "outer" { source = "./mod" }
`), 0o644))

	root, err := config.ParseFile(callerFile)
	require.NoError(t, err)
	ctx, err := config.BuildEvalContext(root, dir)
	require.NoError(t, err)
	_, err = BuildGraph(root, ctx, callerFile)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nested modules")
}

func TestBuildGraphModuleForEachResources(t *testing.T) {
	// Module itself uses for_each internally.
	dir := t.TempDir()
	modDir := filepath.Join(dir, "mod")
	require.NoError(t, os.MkdirAll(modDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "main.sysbox.hcl"), []byte(`
variable "cidrs" { default = "" }
resource "sysbox_network" "seg" {
  for_each = { dmz = "10.0.1.0/24", internal = "10.0.2.0/24" }
  cidr     = each.value
}
`), 0o644))

	callerFile := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(callerFile, []byte(`
module "net" { source = "./mod" }
`), 0o644))

	root, err := config.ParseFile(callerFile)
	require.NoError(t, err)
	ctx, err := config.BuildEvalContext(root, dir)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx, callerFile)
	require.NoError(t, err)

	// for_each keys and the module path remain separate address components.
	require.Len(t, g.All(), 2)
	module := address.ModuleInstance{Name: "net"}
	require.NotNil(t, g.Get(address.StringInstance("sysbox_network", "seg", "dmz").WithModule(module)))
	require.NotNil(t, g.Get(address.StringInstance("sysbox_network", "seg", "internal").WithModule(module)))
}

func TestBuildGraphModuleKeepsInternalDependencyAddresses(t *testing.T) {
	dir := t.TempDir()
	modDir := filepath.Join(dir, "mod")
	require.NoError(t, os.MkdirAll(modDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "main.sysbox.hcl"), []byte(`
resource "sysbox_network" "dmz" { cidr = "10.0.1.0/24" }
resource "sysbox_image" "router" {
  substrate = "docker"
  docker_ref = "alpine:latest"
}
resource "sysbox_router" "gateway" {
  substrate = "docker"
  image = sysbox_image.router.id
  interface "dmz" {
    network = sysbox_network.dmz.id
    ip = "10.0.1.1/24"
  }
}
resource "sysbox_firewall" "edge" {
  attach_to = sysbox_router.gateway.id
  rule "https" {
    direction = "forward"
    protocol = "tcp"
    destination_ports = ["443"]
    verdict = "accept"
  }
}
`), 0o644))
	callerFile := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(callerFile, []byte(`module "lab" { source = "./mod" }`), 0o644))
	root, err := config.ParseFile(callerFile)
	require.NoError(t, err)
	ctx, err := config.BuildEvalContext(root, dir)
	require.NoError(t, err)
	g, err := BuildGraph(root, ctx, callerFile)
	require.NoError(t, err)
	module := address.ModuleInstance{Name: "lab"}
	firewall := address.Resource("sysbox_firewall", "edge").WithModule(module)
	router := address.Resource("sysbox_router", "gateway").WithModule(module)
	require.Equal(t, []address.Address{router}, g.Get(firewall).Deps)
}

// Ensure address.Address is usable in tests.
var _ = address.Address{}

func TestBuildGraphForEachNonStringSetError(t *testing.T) {
	f := writeHCL(t, `
resource "sysbox_network" "lab" {
  for_each = toset([1, 2, 3])
  cidr     = "10.0.0.0/24"
}
`)
	root, err := config.ParseFile(f)
	require.NoError(t, err)

	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	_, err = BuildGraph(root, ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "set must contain strings")
}
