package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestBuildEvalContextReturnsLocalEvaluationErrors(t *testing.T) {
	root, err := ParseString(`
locals {
  broken = missing.value
}
`, "locals.hcl")
	require.NoError(t, err)

	_, err = BuildEvalContext(root)
	require.ErrorContains(t, err, "locals.hcl:3:12")
	require.ErrorContains(t, err, "Variables not allowed")
}

func TestBuildEvalContextReturnsInvalidModuleVariableDefault(t *testing.T) {
	dir := t.TempDir()
	moduleFile := filepath.Join(dir, "module.sysbox.hcl")
	require.NoError(t, os.WriteFile(moduleFile, []byte(`
variable "image" {
  default = missing.value
}
`), 0o600))
	root, err := ParseString(`
module "lab" {
  source = "./module.sysbox.hcl"
}
`, filepath.Join(dir, "root.hcl"))
	require.NoError(t, err)

	_, err = BuildEvalContext(root, dir)
	require.ErrorContains(t, err, "module.sysbox.hcl:3:13")
	require.ErrorContains(t, err, "Variables not allowed")
}

func TestBuildEvalContextReturnsInvalidModuleArgument(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "module.sysbox.hcl"), []byte(`
variable "image" {}
`), 0o600))
	root, err := ParseString(`
module "lab" {
  source = "./module.sysbox.hcl"
  image  = missing.value
}
`, filepath.Join(dir, "root.hcl"))
	require.NoError(t, err)

	_, err = BuildEvalContext(root, dir)
	require.ErrorContains(t, err, "root.hcl:4:12")
	require.ErrorContains(t, err, "Unknown variable")
}

func TestBuildEvalContextReturnsMissingModuleSource(t *testing.T) {
	root, err := ParseString(`
module "missing" {
  source = "./does-not-exist.hcl"
}
`, "root.hcl")
	require.NoError(t, err)

	_, err = BuildEvalContext(root, t.TempDir())
	require.ErrorContains(t, err, "Failed to resolve module source")
}

func TestBuildEvalContextRejectsInvalidCount(t *testing.T) {
	root, err := ParseString(`
resource "sysbox_node" "web" {
  count = "two"
}
`, "count.hcl")
	require.NoError(t, err)

	_, err = BuildEvalContext(root)
	require.ErrorContains(t, err, "count.hcl:3:11")
	require.ErrorContains(t, err, "count must be a non-negative integer")
}

func TestBuildEvalContextExposesZeroCountAsEmptyTuple(t *testing.T) {
	root, err := ParseString(`
resource "sysbox_node" "web" {
  count = 0
}
`, "count.hcl")
	require.NoError(t, err)

	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)
	require.True(t, ctx.Variables["sysbox_node"].GetAttr("web").RawEquals(cty.EmptyTupleVal))
}
