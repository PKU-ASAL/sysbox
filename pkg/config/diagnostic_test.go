package config

import (
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/diag"
)

func TestFromHCLDiagnosticsPreservesSourceRange(t *testing.T) {
	source := hcl.Diagnostics{&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "invalid expression",
		Detail:   "unknown value",
		Subject:  &hcl.Range{Filename: "lab.hcl", Start: hcl.Pos{Line: 3, Column: 7, Byte: 24}, End: hcl.Pos{Line: 3, Column: 12, Byte: 29}},
	}}

	got := fromHCLDiagnostics(source)
	require.Equal(t, diag.Error, got[0].Severity)
	require.Equal(t, "lab.hcl", got[0].Subject.Filename)
	require.Equal(t, 24, got[0].Subject.Start.Byte)
}

func TestParseStringReturnsStructuredDiagnostics(t *testing.T) {
	_, err := ParseString(`resource "sysbox_node" "web" {`, "broken.hcl")
	require.Error(t, err)
	var diagnostics diag.Diagnostics
	require.True(t, errors.As(err, &diagnostics))
	require.Equal(t, "broken.hcl", diagnostics[0].Subject.Filename)
}

func TestDecodeResourceDiagnosticIncludesAddress(t *testing.T) {
	root, err := ParseString(`
resource "sysbox_network" "lab" {
  cidr = missing.value
}
`, "network.hcl")
	require.NoError(t, err)
	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	err = DecodeResource(&root.Resources[0], &NetworkConfig{}, ctx)
	require.Error(t, err)
	var diagnostics diag.Diagnostics
	require.True(t, errors.As(err, &diagnostics))
	require.Equal(t, "sysbox_network.lab", diagnostics[0].Address.String())
}
