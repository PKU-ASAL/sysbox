package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestListResources(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	kernel := filepath.Join(dir, "vmlinux")
	require.NoError(t, os.WriteFile(kernel, []byte("kernel"), 0o644))
	writeState(t, runs, "mixed", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Type:     "sysbox_kernel",
			Name:     "linux",
			Provider: "artifact",
			Instance: map[string]any{"path": kernel},
		}},
	})

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/resources", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Resources []runtime.ResourceHealth `json:"resources"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Resources, 1)
	require.Equal(t, "sysbox_kernel.linux", body.Resources[0].Resource)
	require.Equal(t, runtime.ResourceHealthHealthy, body.Resources[0].Status)
}

func TestGetResourceHealth(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	kernel := filepath.Join(dir, "missing")
	writeState(t, runs, "mixed", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Type:     "sysbox_kernel",
			Name:     "linux",
			Provider: "artifact",
			Instance: map[string]any{"path": kernel},
		}},
	})

	s := NewServer(runs, filepath.Join(dir, "workspaces"))
	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/resources/sysbox_kernel.linux/health", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body runtime.ResourceHealth
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "sysbox_kernel.linux", body.Resource)
	require.Equal(t, runtime.ResourceHealthDrifted, body.Status)
	require.Equal(t, runtime.RecoveryDecisionMarkDrift, body.Decision)
}

func writeState(t *testing.T, runsDir, topology string, st *state.State) {
	t.Helper()
	mgr := state.NewManager(filepath.Join(runsDir, topology, "state.json"))
	require.NoError(t, mgr.Save(st))
}
