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
	"github.com/oslab/sysbox/pkg/persistence"
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
			Address:    address.Resource("sysbox_kernel", "linux"),
			Driver:     "artifact",
			Attributes: map[string]any{"path": kernel},
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

func TestSupervisorScanUsesAgentProjectionForNodeHealth(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "web"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "web", "field.sysbox.hcl"), []byte(""), 0o644))

	// A sysbox_node on the docker substrate: its runtime health probe would need
	// /var/run/docker.sock, which the API container intentionally does not mount.
	// The supervisor must use the agent's reported projection instead of probing
	// docker itself (that probe would fail here and yield Unknown).
	writeState(t, runs, "web", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:    address.Resource("sysbox_node", "web"),
			Driver:     "docker",
			Attributes: map[string]any{"id": "container-1"},
		}},
	})

	s := NewServer(runs, workspaces)
	proj := controlplane.ResourceProjection{
		AgentID: "agent-1", SessionID: "supervisor-session", Sequence: 1,
		Workspace:  "web",
		Topology:   "web",
		ObservedAt: time.Now().UTC(),
		Resources: []controlplane.ResourceHealth{{
			Resource: "sysbox_node.web",
			Type:     "sysbox_node",
			Name:     "web",
			Provider: "docker",
			Status:   controlplane.ResourceHealthHealthy,
		}},
	}
	session, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).OpenSession(context.Background(), "agent-1", "supervisor-process", "")
	require.NoError(t, err)
	proj.SessionID = session.ID
	proj.Sequence = 1
	require.NoError(t, s.apiStore.SaveResourceProjection(context.Background(), proj))

	supervisor := newSupervisor(s, time.Minute)
	require.NoError(t, supervisor.ScanTopology(context.Background(), "web"))

	raw, err := os.ReadFile(filepath.Join(runs, "web", "health.json"))
	require.NoError(t, err)
	var snap HealthSnapshot
	require.NoError(t, json.Unmarshal(raw, &snap))
	require.Equal(t, controlplane.ResourceHealthHealthy, snap.Health.Status)
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

func TestSupervisorRestartOnCrashRequiresApprovedActionPlan(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "mixed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "mixed", "field.sysbox.hcl"), []byte(""), 0o644))
	writeState(t, runs, "mixed", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:    address.Resource("sysbox_kernel", "linux"),
			Driver:     "artifact",
			Attributes: map[string]any{"path": filepath.Join(dir, "missing-vmlinux")},
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
	require.Equal(t, "approval_required", snap.Action)
	require.Empty(t, snap.RunID)
	require.False(t, snap.AutoHeal)
	require.Empty(t, s.jobs.list("mixed"))
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
			Address:    address.Resource("sysbox_kernel", "linux"),
			Driver:     "artifact",
			Attributes: map[string]any{"path": filepath.Join(dir, "missing-vmlinux")},
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
	run := &controlplane.Run{ID: "expired-run", Topology: "mixed", Op: "apply", AgentID: "host-a", Status: controlplane.RunRunning, LeaseOwner: "host-a:run", LeaseUntil: time.Now().UTC().Add(-time.Minute)}
	require.NoError(t, s.apiStore.SaveRun(ctx, *run))
	s.jobs.remember(run)

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
