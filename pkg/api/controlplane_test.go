package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControlPlaneObjects(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	hcl := `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies", bytes.NewBufferString(hcl))
	req.Header.Set("Content-Type", "text/plain")
	q := req.URL.Query()
	q.Set("name", "lab")
	req.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/projects", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"id":"default"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/revisions", nil))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var rev map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rev))
	require.NotEmpty(t, rev["id"])

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/plans", nil))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"workspace":"lab"`)
	require.Contains(t, rec.Body.String(), `"actions"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/lab/stack-state", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"workspace":"lab"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/lab/lease", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestPolicyObjects(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/policies", nil))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"mode":"advisory"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/policies", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"policies"`)
}
