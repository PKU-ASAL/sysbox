package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetTopologyOutputs(t *testing.T) {
	dir := t.TempDir()
	workspaces := filepath.Join(dir, "workspaces")
	runs := filepath.Join(dir, "runs")
	topoDir := filepath.Join(workspaces, "outputs")
	require.NoError(t, os.MkdirAll(topoDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(topoDir, "field.sysbox.hcl"), []byte(`
output "plain" {
  value = "hello"
}

output "with_description" {
  value       = "world"
  description = "shown to clients"
}
`), 0o644))

	s := NewServer(runs, workspaces)
	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/outputs/outputs", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Outputs map[string]struct {
			Value       any    `json:"value"`
			Type        string `json:"type"`
			Description string `json:"description,omitempty"`
		} `json:"outputs"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "hello", body.Outputs["plain"].Value)
	require.Equal(t, "string", body.Outputs["plain"].Type)
	require.Equal(t, "world", body.Outputs["with_description"].Value)
	require.Equal(t, "shown to clients", body.Outputs["with_description"].Description)
}

func TestGetTopologyOutputByName(t *testing.T) {
	dir := t.TempDir()
	workspaces := filepath.Join(dir, "workspaces")
	runs := filepath.Join(dir, "runs")
	topoDir := filepath.Join(workspaces, "outputs")
	require.NoError(t, os.MkdirAll(topoDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(topoDir, "field.sysbox.hcl"), []byte(`
output "answer" {
  value = "42"
}
`), 0o644))

	s := NewServer(runs, workspaces)
	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/outputs/outputs?name=answer", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"answer"`)
	require.NotContains(t, rec.Body.String(), `"missing"`)
}
