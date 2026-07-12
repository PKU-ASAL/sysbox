package diag

import (
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/stretchr/testify/require"
)

func TestDiagnosticsSortAndFormatDeterministically(t *testing.T) {
	resource := address.Resource("sysbox_node", "web")
	diagnostics := Diagnostics{
		{Severity: Error, Summary: "later", Detail: "detail b", Subject: &SourceRange{Filename: "b.hcl", Start: SourcePos{Line: 2, Column: 3, Byte: 12}}},
		{Severity: Warning, Summary: "warning", Detail: "detail w", Subject: &SourceRange{Filename: "a.hcl", Start: SourcePos{Line: 2, Column: 1, Byte: 8}}},
		{Severity: Error, Summary: "first", Detail: "detail a", Subject: &SourceRange{Filename: "a.hcl", Start: SourcePos{Line: 1, Column: 4, Byte: 3}}, Address: &resource},
	}

	diagnostics.Sort()
	require.Equal(t, "first", diagnostics[0].Summary)
	require.Equal(t, "warning", diagnostics[1].Summary)
	require.Equal(t, "later", diagnostics[2].Summary)
	require.True(t, diagnostics.HasErrors())
	require.Equal(t,
		"a.hcl:1:4: error: first [sysbox_node.web]: detail a\n"+
			"a.hcl:2:1: warning: warning: detail w\n"+
			"b.hcl:2:3: error: later: detail b",
		diagnostics.Error(),
	)
}

func TestWarningsDoNotProduceError(t *testing.T) {
	diagnostics := Diagnostics{{Severity: Warning, Summary: "deprecated"}}
	require.False(t, diagnostics.HasErrors())
	require.NoError(t, diagnostics.Err())
}
