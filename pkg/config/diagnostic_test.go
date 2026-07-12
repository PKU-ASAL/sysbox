package config

import (
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
