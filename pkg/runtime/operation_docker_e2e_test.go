//go:build e2e

package runtime_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/persistence"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// This gate refuses the ordinary host daemon. Run it inside a disposable Docker
// daemon carrying the exact acceptance label; no shared resource cleanup occurs.
func TestResourceOperationDockerLifecycleE2E(t *testing.T) {
	if os.Getenv("SYSBOX_RESOURCE_CORE_E2E") != "1" {
		t.Skip("requires isolated resource-core Docker daemon")
	}
	require.NotEmpty(t, os.Getenv("DOCKER_HOST"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	t.Cleanup(func() { _ = api.Close() })
	info, err := api.Info(ctx)
	require.NoError(t, err)
	require.Contains(t, info.Labels, "sysbox.resource-core.acceptance=true", "never use a shared daemon for this gate")
	imageReference := os.Getenv("SYSBOX_DOCKER_E2E_IMAGE")
	if imageReference == "" {
		imageReference = "sysbox-resource-core:fixture"
	}
	image, _, err := api.ImageInspectWithRaw(ctx, imageReference)
	require.NoError(t, err, "preload a local image with a native long-running ENTRYPOINT; gate never pulls")
	sub, err := dockerprovider.New()
	require.NoError(t, err)
	defer sub.Close()
	providers := driver.NewRegistry()
	require.NoError(t, providers.Register(driver.Descriptor{Name: "docker", Version: "1", Node: sub, NIC: sub, NodeState: sub, Artifact: sub, ImageEntry: sub, Network: sub, GuestNetwork: sub, Reset: sub}))
	directory, err := os.MkdirTemp("", "sysbox-resource-core-")
	require.NoError(t, err)
	t.Logf("retained evidence: %s", directory)
	topology := "core-" + uuid.NewString()
	// Failed runs may leave uncertain objects. Cleanup queries and removes only
	// resources carrying this exact, unique topology label on the gated daemon.
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		filter := filters.NewArgs(filters.Arg("label", "sysbox.topology="+topology))
		containers, listErr := api.ContainerList(cleanupCtx, container.ListOptions{All: true, Filters: filter})
		if listErr == nil {
			for _, item := range containers {
				if item.Labels["sysbox.topology"] == topology {
					_ = api.ContainerRemove(cleanupCtx, item.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
				}
			}
		}
		networks, listErr := api.NetworkList(cleanupCtx, network.ListOptions{Filters: filter})
		if listErr == nil {
			for _, item := range networks {
				if item.Labels["sysbox.topology"] == topology {
					_ = api.NetworkRemove(cleanupCtx, item.ID)
				}
			}
		}
	})
	path := filepath.Join(directory, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`substrate "docker" { alias = "local" }
resource "sysbox_image" "base" {
 substrate = substrate.docker.local
 kind = "oci"
 source = %q
 sha256 = %q
 architecture = %q
 guest_family = "linux"
}
resource "sysbox_network" "lab" {
 cidr = "10.231.42.0/24"
 nat = true
}
resource "sysbox_node" "web" {
 substrate = substrate.docker.local
 image = sysbox_image.base.id
 link "lab" {
  network = sysbox_network.lab.id
  ip = "10.231.42.10/24"
 }
}
`, imageReference, image.ID, goruntime.GOARCH)), 0600))
	repo, err := persistence.OpenConfigured(ctx, "sqlite://"+filepath.Join(directory, "state.db"), "")
	require.NoError(t, err)
	defer repo.Close()
	manager := state.NewManagerWithBackend(repo.StateBackend(topology))
	run := func(kind string, changes bool) *state.State {
		t.Helper()
		unlock, err := repo.LockExecution(ctx, topology)
		require.NoError(t, err)
		defer unlock()
		req := runtime.OperationRequest{ID: uuid.NewString(), Topology: topology, Kind: kind, Workspace: path, QueuedAt: time.Now().UTC(), Revision: "acceptance"}
		op, err := runtime.PrepareOperation(ctx, req, runtime.OperationDependencies{Providers: providers, Manager: manager, Actions: repo, Log: io.Discard})
		require.NoError(t, err)
		plan, err := op.Plan(ctx)
		require.NoError(t, err)
		require.Equal(t, changes, plan.HasChanges())
		if executeErr := op.Execute(ctx, plan); executeErr != nil {
			actions, loadErr := repo.LoadActions(context.Background(), topology)
			t.Logf("operation=%s error=%v journal_error=%v", kind, executeErr, loadErr)
			for _, action := range actions {
				t.Logf("action=%s resource=%s status=%s error=%s", action.ID, action.Resource, action.Status, action.Error)
			}
			t.FailNow()
		}
		current, err := manager.LoadWithContext(ctx)
		require.NoError(t, err)
		return current
	}
	first := run("apply", true)
	node := address.Resource("sysbox_node", "web")
	networkAddress := address.Resource("sysbox_network", "lab")
	old := *first.FindResource(node)
	netID := first.FindResource(networkAddress).ExternalID
	require.NotEmpty(t, old.ExternalID)
	require.NotEmpty(t, netID)
	require.Equal(t, old.ExternalID, run("apply", false).FindResource(node).ExternalID)
	replacement := run("reset", true).FindResource(node)
	require.NotEqual(t, old.ExternalID, replacement.ExternalID)
	require.Greater(t, replacement.Generation, old.Generation)
	_, err = api.ContainerInspect(ctx, old.ExternalID)
	require.True(t, errdefs.IsNotFound(err), "old generation still exists: %v", err)
	require.Empty(t, run("destroy", true).Resources)
	_, err = api.ContainerInspect(ctx, replacement.ExternalID)
	require.True(t, errdefs.IsNotFound(err), "replacement still exists: %v", err)
	_, err = api.NetworkInspect(ctx, netID, network.InspectOptions{})
	require.True(t, errdefs.IsNotFound(err), "owned network still exists: %v", err)
}
