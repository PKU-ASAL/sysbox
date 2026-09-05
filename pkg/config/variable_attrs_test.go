package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// parseVariable parses a single top-level variable block and returns it, so a
// test can inspect its decoded fields.
func parseVariable(t *testing.T, body string) *VariableBlock {
	t.Helper()
	root, err := ParseString("variable \"x\" {\n"+body+"\n}\n", "test.hcl")
	require.NoError(t, err)
	require.Len(t, root.Variables, 1)
	return &root.Variables[0]
}

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

// sensitive is a first-class attribute: it decodes into the block's Sensitive
// field, which is what S3 (sensitive variables) reads.
func TestVariableBlockDecodesSensitive(t *testing.T) {
	vb := parseVariable(t, "sensitive = true\n  default = \"x\"")

	require.True(t, vb.Sensitive)
}

// A variable without sensitive is not sensitive.
func TestVariableBlockSensitiveDefaultsFalse(t *testing.T) {
	vb := parseVariable(t, "default = \"x\"")

	require.False(t, vb.Sensitive)
}

// type is declared but not enforced: it is captured as a raw expression, so the
// author can write the Terraform-style `type = string` without a decode error.
func TestVariableBlockDecodesType(t *testing.T) {
	vb := parseVariable(t, "type = string\n  default = \"x\"")

	require.NotNil(t, vb.Type)
}

// A genuinely unknown attribute is still rejected: the point of the original
// check was to never silently drop an attribute, and that still holds for
// attributes sysbox does not implement.
func TestVariableBlockRejectsUnknownAttribute(t *testing.T) {
	root, dir := moduleWithVariable(t, `
variable "flag" {
  description = "not implemented"
}
`, "")

	_, err := BuildEvalContext(root, dir)

	require.Error(t, err, "an attribute sysbox does not implement must be rejected, not dropped")
	require.ErrorContains(t, err, "description")
}

// The rejection must name the offending block, or the author cannot find it.
func TestVariableBlockRejectionNamesTheVariable(t *testing.T) {
	root, dir := moduleWithVariable(t, `
variable "flag" {
  description = "not implemented"
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
