package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

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

func TestSelectWorkerByCapabilities(t *testing.T) {
	s := &Server{agents: newAgentRegistry()}
	ctx := context.Background()
	s.agents.Save(controlplane.Worker{
		ID:           "docker-worker",
		Status:       "online",
		Capabilities: []string{"docker"},
	})
	s.agents.Save(controlplane.Worker{
		ID:           "vm-worker",
		Status:       "online",
		Capabilities: []string{"docker", "network", "kvm", "firecracker"},
	})

	worker, err := s.selectWorker(ctx, []string{"firecracker", "kvm", "network"})
	require.NoError(t, err)
	require.Equal(t, "vm-worker", worker.ID)

	_, err = s.selectWorker(ctx, []string{"gpu"})
	require.ErrorContains(t, err, "no online worker")
}

func TestDispatchRunAssignsWorkerBeforeExecution(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("mixed", "apply")

	err := s.dispatchRun(context.Background(), run, []string{"docker"})
	require.NoError(t, err)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, DefaultWorkerID, got.WorkerID)
	require.Equal(t, RunAssigned, got.Status)
	require.False(t, got.AssignedAt.IsZero())
}

func TestWorkerClaimRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("mixed", "apply")
	require.NoError(t, s.dispatchRun(context.Background(), run, []string{"docker"}))

	assigned := s.assignedRunsForWorker(context.Background(), DefaultWorkerID)
	require.Len(t, assigned, 1)
	require.Equal(t, run.ID, assigned[0].ID)

	claimed, err := s.jobs.claim(run.ID, DefaultWorkerID)
	require.NoError(t, err)
	require.Equal(t, RunRunning, claimed.Status)

	assigned = s.assignedRunsForWorker(context.Background(), DefaultWorkerID)
	require.Empty(t, assigned)
}
