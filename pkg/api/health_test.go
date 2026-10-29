package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

func TestGetTopologyHealth(t *testing.T) {
	dir := t.TempDir()
	workspaces := filepath.Join(dir, "workspaces")
	runs := filepath.Join(dir, "runs")
	topo := filepath.Join(workspaces, "health")
	require.NoError(t, os.MkdirAll(topo, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(topo, "field.sysbox.hcl"), []byte(``), 0o644))

	kernel := filepath.Join(dir, "vmlinux")
	require.NoError(t, os.WriteFile(kernel, []byte("kernel"), 0o644))
	st := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:    address.Resource("sysbox_kernel", "linux"),
			Driver:     "artifact",
			Attributes: map[string]any{"path": kernel},
		}},
	}
	writeState(t, runs, "health", st)

	s := NewServer(runs, workspaces)
	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/health/health", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body model.TopologyHealth
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, model.ResourceHealthHealthy, body.Status)
	require.Equal(t, 1, body.Healthy)
	require.Len(t, body.Resources, 1)
	require.Equal(t, "sysbox_kernel.linux", body.Resources[0].Resource)
}

func TestGetTopologyHealthMissingState(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(filepath.Join(dir, "runs"), filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/missing/health", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}
