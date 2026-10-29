package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBoundaryWorkerCannotCompleteRunWithoutHost(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.API.Token = "admin-only"
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "worker", AuthSecret: "worker-secret", SecretHash: agent.SecretHash("worker-secret")}))
	now := time.Now().UTC()
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "worker", Status: controlplane.RunRunning, Op: "apply", Operation: "apply", Attempt: 1, LeaseOwner: "worker-process", LeaseUntil: now.Add(time.Minute), QueuedAt: now.Add(-time.Minute), StartedAt: now.Add(-time.Second)}
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	run.Status = controlplane.RunDone
	run.EndedAt = now
	raw, err := json.Marshal(map[string]any{"run": run})
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/v1/agents/worker/runs/run/complete", bytes.NewReader(raw))
	require.NoError(t, agent.SignRequest(req, "worker", "worker-secret", now))
	w := httptest.NewRecorder()
	authMiddleware(s).ServeHTTP(w, req)
	require.NotEqual(t, 200, w.Code, w.Body.String())
	stored, err := s.apiStore.GetRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, controlplane.RunRunning, stored.Status)
}
func TestBoundaryWorkerCannotForgeHostCapabilities(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.API.Token = "admin-only"
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "worker", AuthSecret: "worker-secret", SecretHash: agent.SecretHash("worker-secret"), Capabilities: []string{"docker"}}))
	raw, err := json.Marshal(controlplane.Agent{Capabilities: []string{"firecracker", "kvm"}})
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/v1/agents/worker/heartbeat", bytes.NewReader(raw))
	require.NoError(t, agent.SignRequest(req, "worker", "worker-secret", time.Now()))
	w := httptest.NewRecorder()
	authMiddleware(s).ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	stored, err := s.apiStore.GetAgent(ctx, "worker")
	require.NoError(t, err)
	require.Equal(t, []string{"docker"}, stored.Capabilities)
}

func TestBoundaryWorkerCannotAdmitAdoption(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "libvirt", Version: "test", OwnershipAdoption: libvirtprovider.New(t.TempDir())}))
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.API.Token = "admin-only"
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "worker", AuthSecret: "worker-secret", SecretHash: agent.SecretHash("worker-secret"), Capabilities: []string{"libvirt"}, LastHeartbeat: time.Now().UTC()}))
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
	st, err = repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	request := controlplane.AdoptionRequest{ID: "worker-admitted", Topology: "lab", TopologyUID: st.TopologyUID, Address: "sysbox_node.imported", Provider: "libvirt", AgentID: "worker", ExternalID: "unreviewed", ResourceUID: "resource", Generation: 1, ExpectedSerial: st.Meta.Serial, Evidence: substrate.AdoptionEvidence{Provider: "libvirt", ExternalID: "unreviewed", Fingerprint: "unreviewed"}}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/v1/agents/worker/adoption-confirm", bytes.NewReader(raw))
	require.NoError(t, agent.SignRequest(req, "worker", "worker-secret", time.Now()))
	w := httptest.NewRecorder()
	authMiddleware(s).ServeHTTP(w, req)
	require.NotEqual(t, 202, w.Code, w.Body.String())
}

func TestBoundaryWorkerCannotReverseHostObservationOrder(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.API.Token = "admin-only"
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "worker", AuthSecret: "worker-secret", SecretHash: agent.SecretHash("worker-secret")}))
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"worker": pub}
	repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := repo.OpenSession(ctx, "worker", "process", "")
	require.NoError(t, err)
	var cpu int64 = 1
	observer := hostsupervisor.Observer{AgentID: "worker", BootID: "host", Key: key, Collect: func(context.Context) (controlplane.AgentInventory, []controlplane.ResourceProjection, error) {
		return controlplane.AgentInventory{Resources: controlplane.HostResourceInventory{CPU: cpu}}, nil, nil
	}}
	older, err := observer.Snapshot(ctx, hostsupervisor.ObservationRequest{SessionID: session.ID})
	require.NoError(t, err)
	cpu = 2
	newer, err := observer.Snapshot(ctx, hostsupervisor.ObservationRequest{SessionID: session.ID})
	require.NoError(t, err)
	require.Greater(t, newer.Sequence, older.Sequence)
	post := func(o controlplane.HostObservation) int {
		raw, err := json.Marshal(o)
		require.NoError(t, err)
		req := httptest.NewRequest("POST", "/v1/agents/worker/observations", bytes.NewReader(raw))
		require.NoError(t, agent.SignRequest(req, "worker", "worker-secret", time.Now()))
		w := httptest.NewRecorder()
		authMiddleware(s).ServeHTTP(w, req)
		return w.Code
	}
	require.Equal(t, 202, post(newer))
	require.NotEqual(t, 202, post(older), "host collection order must reject older signed snapshots")
}
