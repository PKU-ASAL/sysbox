package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// moduleWithVariable writes a module file and a root that calls it, returning
// the root and the directory to resolve the module source against.
func moduleWithVariable(t *testing.T, moduleBody, rootArgs string) (*Root, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "module.sysbox.hcl"), []byte(moduleBody), 0o600))
	root, err := ParseString(`
module "lab" {
  source = "./module.sysbox.hcl"
`+rootArgs+`
}
`, filepath.Join(dir, "root.hcl"))
	require.NoError(t, err)
	return root, dir
}

// Variable blocks are decoded with JustAttributes and only "default" is ever
// consumed, so every other attribute is read and then dropped on the floor.
//
// An author who writes `sensitive = true` gets a value that is still written to
// state, outputs and logs. Silently accepting an attribute that does nothing is
// worse than rejecting it: the author has no way to discover that the guarantee
// they asked for was never applied.
func TestVariableBlockRejectsUnsupportedAttribute(t *testing.T) {
	root, dir := moduleWithVariable(t, `
variable "flag" {
  sensitive = true
}
`, "")

	_, err := BuildEvalContext(root, dir)

	require.Error(t, err,
		"sensitive is not implemented; accepting it silently promises a guarantee that is never applied")
	require.ErrorContains(t, err, "sensitive")
}

// The same for a type constraint: unimplemented must not look like accepted.
func TestVariableBlockRejectsTypeConstraint(t *testing.T) {
	root, dir := moduleWithVariable(t, `
variable "cidr" {
  type    = string
  default = "10.0.0.0/24"
}
`, "")

	_, err := BuildEvalContext(root, dir)

	require.Error(t, err, "type constraints are not enforced; do not accept them silently")
	require.ErrorContains(t, err, "type")
}

// The rejection must name the offending block, or the author cannot find it.
func TestVariableBlockRejectionNamesTheVariable(t *testing.T) {
	root, dir := moduleWithVariable(t, `
variable "flag" {
  sensitive = true
}
`, "")

	_, err := BuildEvalContext(root, dir)

	require.ErrorContains(t, err, "flag")
}

// The supported shape must keep working unchanged — this is what every existing
// topology uses.
func TestVariableBlockAcceptsDefault(t *testing.T) {
	root, dir := moduleWithVariable(t, `
variable "cidr" {
  default = "10.0.0.0/24"
}
`, "")

	_, err := BuildEvalContext(root, dir)

	require.NoError(t, err, "default is the supported attribute and must keep working")
}

// A bare variable block declares an input with no default and stays legal.
func TestVariableBlockAcceptsNoAttributes(t *testing.T) {
	root, dir := moduleWithVariable(t, `
variable "image" {}
`, `  image = "alpine"`)

	_, err := BuildEvalContext(root, dir)

	require.NoError(t, err, "a bare variable block must stay legal")
}
