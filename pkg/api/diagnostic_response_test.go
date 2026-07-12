package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/diag"
)

func TestWriteErrorReturnsStructuredDiagnostics(t *testing.T) {
	recorder := httptest.NewRecorder()
	diagnostics := diag.Diagnostics{{
		Severity: diag.Error,
		Summary:  "Invalid expression",
		Subject:  &diag.SourceRange{Filename: "lab.hcl", Start: diag.SourcePos{Line: 2, Column: 4}},
	}}

	writeError(recorder, http.StatusUnprocessableEntity, fmt.Errorf("validate topology: %w", diagnostics))

	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	var response struct {
		Error       string           `json:"error"`
		Diagnostics diag.Diagnostics `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Contains(t, response.Error, "validate topology")
	require.Equal(t, "lab.hcl", response.Diagnostics[0].Subject.Filename)
}

func TestWorkspaceStatusUsesUnprocessableEntityForDiagnostics(t *testing.T) {
	diagnostics := diag.Diagnostics{{Severity: diag.Error, Summary: "Invalid HCL"}}
	require.Equal(t, http.StatusUnprocessableEntity, workspaceStatus(fmt.Errorf("invalid HCL: %w", diagnostics)))
}
