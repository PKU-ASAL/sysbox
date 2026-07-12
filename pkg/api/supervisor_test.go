package api

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/address"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
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
			Address:  address.Resource("sysbox_kernel", "linux"),
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
	require.Equal(t, controlplane.ResourceHealthHealthy, snap.Health.Status)
	require.Equal(t, SupervisorPolicyObserveOnly, snap.Policy)
	require.Equal(t, "observe", snap.Action)
}

func TestGetTopologyHealthCached(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	s := NewServer(runs, workspaces)
	snap := HealthSnapshot{
		Topology: "mixed",
		Observed: time.Now().UTC(),
		Health: controlplane.TopologyHealth{
			Status: controlplane.ResourceHealthHealthy,
		},
		Policy: SupervisorPolicyObserveOnly,
	}
	require.NoError(t, s.saveHealthSnapshot("mixed", snap))

	req := httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/health?cached=true", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body HealthSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "mixed", body.Topology)
	require.Equal(t, controlplane.ResourceHealthHealthy, body.Health.Status)
}

func TestSupervisorRestartOnCrashStartsApplyForDriftedTopology(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "mixed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "mixed", "field.sysbox.hcl"), []byte(""), 0o644))
	writeState(t, runs, "mixed", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:  address.Resource("sysbox_kernel", "linux"),
			Provider: "artifact",
			Instance: map[string]any{"path": filepath.Join(dir, "missing-vmlinux")},
		}},
	})

	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = runs
	cfg.Paths.WorkspacesDir = workspaces
	cfg.Supervisor.Policy = string(SupervisorPolicyRestartOnCrash)
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID:           "host-a",
		Status:       "online",
		Capabilities: []string{"docker"},
	}))
	supervisor := newSupervisor(s, time.Minute)
	require.NoError(t, supervisor.ScanTopology(context.Background(), "mixed"))

	snap, err := s.loadHealthSnapshot("mixed")
	require.NoError(t, err)
	require.Equal(t, SupervisorPolicyRestartOnCrash, snap.Policy)
	require.Equal(t, "restart_apply_started", snap.Action)
	require.NotEmpty(t, snap.RunID)
	run, ok := s.jobs.get(snap.RunID)
	require.True(t, ok)
	require.Equal(t, "supervisor", run.ParentID)
	require.Equal(t, "apply", run.Op)
	require.Equal(t, controlplane.RunAssigned, run.Status)
}

func TestSupervisorRestartOnCrashSkipsWhenRunActive(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "mixed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "mixed", "field.sysbox.hcl"), []byte(""), 0o644))
	writeState(t, runs, "mixed", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:  address.Resource("sysbox_kernel", "linux"),
			Provider: "artifact",
			Instance: map[string]any{"path": filepath.Join(dir, "missing-vmlinux")},
		}},
	})

	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = runs
	cfg.Paths.WorkspacesDir = workspaces
	cfg.Supervisor.Policy = string(SupervisorPolicyRestartOnCrash)
	s := NewServerWithConfig(cfg)
	_ = s.jobs.start("mixed", "apply")
	supervisor := newSupervisor(s, time.Minute)
	require.NoError(t, supervisor.ScanTopology(context.Background(), "mixed"))

	snap, err := s.loadHealthSnapshot("mixed")
	require.NoError(t, err)
	require.Equal(t, "skipped_running_operation", snap.Action)
	require.Empty(t, snap.RunID)
}

func TestSupervisorMarksStaleAgentOfflineAndExpiredRunFailed(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.Agent.Lease.OfflineAfter = "5m"
	s := NewServerWithConfig(cfg)
	ctx := context.Background()
	require.NoError(t, s.agentService().Save(ctx, controlplane.Agent{
		ID:            "host-a",
		Status:        "online",
		Capabilities:  []string{"docker"},
		LastHeartbeat: time.Now().UTC().Add(-10 * time.Minute),
	}))
	run := s.jobs.start("mixed", "apply")
	run.AgentID = "host-a"
	run.Status = controlplane.RunRunning
	run.LeaseOwner = "host-a:run"
	run.LeaseUntil = time.Now().UTC().Add(-time.Minute)
	s.jobs.replace(run)

	supervisor := newSupervisor(s, time.Minute)
	supervisor.server.agentService().MarkStaleOffline(ctx, time.Now().UTC())
	supervisor.server.jobs.markExpiredLeases(time.Now().UTC())

	agent, err := s.apiStore.GetAgent(ctx, "host-a")
	require.NoError(t, err)
	require.Equal(t, "offline", agent.Status)
	require.Equal(t, "heartbeat stale", agent.Reason)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunFailed, got.Status)
	require.Equal(t, "run lease expired", got.Err)
	require.True(t, got.Recoverable)
}
