package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestRequiredCapabilitiesForTopology(t *testing.T) {
	dir := t.TempDir()
	hcl := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(hcl, []byte(`
resource "sysbox_network" "shared" {
  cidr = "10.99.0.0/24"
}

resource "sysbox_image" "rootfs" {
  substrate = "firecracker"
  rootfs = "/tmp/rootfs.ext4"
}

resource "sysbox_node" "microvm" {
  substrate = "firecracker"
  image = sysbox_image.rootfs.id
  link {
    network = sysbox_network.shared.id
    ip = "10.99.0.10/24"
  }
}
`), 0o644))

	caps, err := requiredCapabilitiesForTopology(hcl)
	require.NoError(t, err)
	require.Equal(t, []string{"firecracker", "kvm", "network"}, caps)
}

func TestSelectAgentByCapabilities(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{
		ID:           "docker-agent",
		Status:       "online",
		Capabilities: []string{"docker"},
	}))
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{
		ID:           "vm-agent",
		Status:       "online",
		Capabilities: []string{"docker", "network", "kvm", "firecracker"},
	}))

	agent, err := s.selectAgent(ctx, []string{"firecracker", "kvm", "network"})
	require.NoError(t, err)
	require.Equal(t, "vm-agent", agent.ID)

	_, err = s.selectAgent(ctx, []string{"gpu"})
	require.ErrorContains(t, err, "no online agent")
}

func TestDispatchRunAssignsAgentBeforeExecution(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("mixed", "apply")

	err := s.dispatchRun(context.Background(), run, []string{"docker"})
	require.NoError(t, err)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, DefaultAgentID, got.AgentID)
	require.Equal(t, RunAssigned, got.Status)
	require.False(t, got.AssignedAt.IsZero())
}

func TestAgentClaimRun(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.Run.Lease.ClaimTTL = "2m"
	s := NewServerWithConfig(cfg)
	run := s.jobs.start("mixed", "apply")
	require.NoError(t, s.dispatchRun(context.Background(), run, []string{"docker"}))

	commands, err := s.apiStore.ListAgentCommands(context.Background(), DefaultAgentID)
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Equal(t, "run_assigned", commands[0].Type)
	require.Equal(t, run.ID, commands[0].Run.ID)

	claimed, err := s.jobs.claim(run.ID, DefaultAgentID)
	require.NoError(t, err)
	require.Equal(t, RunRunning, claimed.Status)
	require.Equal(t, 1, claimed.Attempt)
	require.NotEmpty(t, claimed.LeaseOwner)
	require.False(t, claimed.LeaseUntil.IsZero())
	require.WithinDuration(t, time.Now().UTC().Add(2*time.Minute), claimed.LeaseUntil, 5*time.Second)

	_, err = s.jobs.claim(run.ID, DefaultAgentID)
	require.ErrorContains(t, err, "cannot be claimed")
}

func TestAgentRenewRunLeaseEndpoint(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.Run.Lease.RenewTTL = "3m"
	s := NewServerWithConfig(cfg)
	run := s.jobs.start("mixed", "apply")
	require.NoError(t, s.dispatchRun(context.Background(), run, []string{"docker"}))
	claimed, err := s.jobs.claim(run.ID, DefaultAgentID)
	require.NoError(t, err)
	before := claimed.LeaseUntil

	body := bytes.NewBufferString(`{"lease_owner":"` + claimed.LeaseOwner + `","ttl_seconds":3600}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/local/runs/"+run.ID+"/renew", body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.True(t, got.LeaseUntil.After(before))
}

func TestAgentRenewRunLeaseEndpointUsesConfiguredDefaultTTL(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.Run.Lease.RenewTTL = "90s"
	s := NewServerWithConfig(cfg)
	run := s.jobs.start("mixed", "apply")
	require.NoError(t, s.dispatchRun(context.Background(), run, []string{"docker"}))
	claimed, err := s.jobs.claim(run.ID, DefaultAgentID)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"lease_owner":"` + claimed.LeaseOwner + `"}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/local/runs/"+run.ID+"/renew", body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.WithinDuration(t, time.Now().UTC().Add(90*time.Second), got.LeaseUntil, 5*time.Second)
}
