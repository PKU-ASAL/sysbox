package config

import (
	"testing"

	"github.com/stretchr/testify/require"
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
