package diag

import (
	"fmt"
	"sort"
	"strings"
)

type Diagnostics []Diagnostic

func (d Diagnostics) Sort() {
	sort.SliceStable(d, func(i, j int) bool {
		return diagnosticSortKey(d[i]) < diagnosticSortKey(d[j])
	})
}

func (d Diagnostics) HasErrors() bool {
	for _, diagnostic := range d {
		if diagnostic.Severity == Error {
			return true
		}
	}
	return false
}

func (d Diagnostics) Err() error {
	if !d.HasErrors() {
		return nil
	}
	return d
}

func (d Diagnostics) Error() string {
	lines := make([]string, 0, len(d))
	for _, diagnostic := range d {
		lines = append(lines, formatDiagnostic(diagnostic))
	}
	return strings.Join(lines, "\n")
}

func diagnosticSortKey(d Diagnostic) string {
	filename := ""
	line, column, offset := 0, 0, 0
	if d.Subject != nil {
		filename = d.Subject.Filename
		line = d.Subject.Start.Line
		column = d.Subject.Start.Column
		offset = d.Subject.Start.Byte
	}
	resource := ""
	if d.Address != nil {
		resource = d.Address.String()
	}
	return fmt.Sprintf("%s\x00%010d\x00%010d\x00%010d\x00%s\x00%s\x00%s\x00%s", filename, line, column, offset, d.Severity, resource, d.Summary, d.Detail)
}

func formatDiagnostic(d Diagnostic) string {
	var output strings.Builder
	if d.Subject != nil {
		fmt.Fprintf(&output, "%s:%d:%d: ", d.Subject.Filename, d.Subject.Start.Line, d.Subject.Start.Column)
	}
	fmt.Fprintf(&output, "%s: %s", d.Severity, d.Summary)
	if d.Address != nil {
		fmt.Fprintf(&output, " [%s]", d.Address.String())
	}
	if d.Detail != "" {
		fmt.Fprintf(&output, ": %s", d.Detail)
	}
	return output.String()
}
