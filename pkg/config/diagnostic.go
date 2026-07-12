package config

import (
	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/diag"
)

func fromHCLDiagnostics(source hcl.Diagnostics) diag.Diagnostics {
	result := make(diag.Diagnostics, 0, len(source))
	for _, item := range source {
		severity := diag.Warning
		if item.Severity == hcl.DiagError {
			severity = diag.Error
		}
		result = append(result, diag.Diagnostic{
			Severity: severity,
			Summary:  item.Summary,
			Detail:   item.Detail,
			Subject:  fromHCLRange(item.Subject),
		})
	}
	return result
}

func fromHCLRange(source *hcl.Range) *diag.SourceRange {
	if source == nil {
		return nil
	}
	return &diag.SourceRange{
		Filename: source.Filename,
		Start:    diag.SourcePos{Line: source.Start.Line, Column: source.Start.Column, Byte: source.Start.Byte},
		End:      diag.SourcePos{Line: source.End.Line, Column: source.End.Column, Byte: source.End.Byte},
	}
}
