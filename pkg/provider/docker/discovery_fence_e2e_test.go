//go:build e2e

package docker

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func TestDockerLostCreateIdentityDiscoveryE2E(t *testing.T) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires isolated Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	name := fmt.Sprintf("lost-create-%d", time.Now().UnixNano())
	id := substrate.OperationIdentity{ActionID: "original-create", OwnerUID: name, TopologyUID: name, ResourceUID: "node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	image, _, err := s.cli.ImageInspectWithRaw(ctx, "alpine:3.22")
	require.NoError(t, err)
	created, err := s.CreateNode(ctx, substrate.NodeSpec{Name: name, Image: substrate.ArtifactHandle{ID: image.ID}, ManagedNetwork: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		err := s.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
		if !errdefs.IsNotFound(err) {
			require.NoError(t, err)
		}
	})
	// Discard the returned identity: recover from the durable original scope/name.
	found, err := s.DiscoverManagedNode(ctx, id, name)
	require.NoError(t, err)
	require.Equal(t, created.ID, found.ID)
	before, err := s.cli.ContainerInspect(ctx, created.ID)
	require.NoError(t, err)
	again, err := s.DiscoverManagedNode(ctx, id, name)
	require.NoError(t, err)
	require.Equal(t, found.ID, again.ID)
	after, err := s.cli.ContainerInspect(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, before.State, after.State)
	foreign := id
	foreign.Generation++
	_, err = s.DiscoverManagedNode(substrate.WithOperationIdentity(ctx, foreign), foreign, name)
	require.Error(t, err)
	t.Log("lost create identity rediscovered by exact owner/UID/generation; repeated query preserves container, foreign generation refused")
}
