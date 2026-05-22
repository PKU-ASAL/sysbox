package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestSupervisorScanWritesHealthSnapshot(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "mixed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "mixed", "field.sysbox.hcl"), []byte(""), 0o644))

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

	s := NewServer(runs, workspaces)
	supervisor := newSupervisor(s, time.Minute)
	require.NoError(t, supervisor.ScanTopology(context.Background(), "mixed"))

	raw, err := os.ReadFile(filepath.Join(runs, "mixed", "health.json"))
	require.NoError(t, err)
	var snap HealthSnapshot
	require.NoError(t, json.Unmarshal(raw, &snap))
	require.Equal(t, "mixed", snap.Topology)
	require.Equal(t, runtime.ResourceHealthHealthy, snap.Health.Status)
	require.Equal(t, "observe_only", snap.Policy)
}

func TestGetTopologyHealthCached(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	s := NewServer(runs, workspaces)
	snap := HealthSnapshot{
		Topology: "mixed",
		Observed: time.Now().UTC(),
		Health: runtime.TopologyHealth{
			Status: runtime.ResourceHealthHealthy,
		},
		Policy: "observe_only",
	}
	require.NoError(t, s.saveHealthSnapshot("mixed", snap))

	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/health?cached=true", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body HealthSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "mixed", body.Topology)
	require.Equal(t, runtime.ResourceHealthHealthy, body.Health.Status)
}
