package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	store := &localAPIStore{runsDir: t.TempDir()}
	s := &Server{apiStore: store}
	ctx := context.Background()
	require.NoError(t, store.SaveWorker(ctx, controlplane.Worker{
		ID:           "docker-worker",
		Status:       "online",
		Capabilities: []string{"docker"},
	}))
	require.NoError(t, store.SaveWorker(ctx, controlplane.Worker{
		ID:           "vm-worker",
		Status:       "online",
		Capabilities: []string{"docker", "network", "kvm", "firecracker"},
	}))

	worker, err := s.selectWorker(ctx, []string{"firecracker", "kvm", "network"})
	require.NoError(t, err)
	require.Equal(t, "vm-worker", worker.ID)

	_, err = s.selectWorker(ctx, []string{"gpu"})
	require.ErrorContains(t, err, "no online worker")
}

func TestDispatchRunAssignsWorkerBeforeExecution(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("mixed", "apply")
	done := make(chan struct{})

	err := s.dispatchRun(context.Background(), run, []string{"docker"}, func(run *Run) {
		require.Equal(t, DefaultWorkerID, run.WorkerID)
		require.Equal(t, RunRunning, run.Status)
		close(done)
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, DefaultWorkerID, got.WorkerID)
	require.False(t, got.AssignedAt.IsZero())
}
