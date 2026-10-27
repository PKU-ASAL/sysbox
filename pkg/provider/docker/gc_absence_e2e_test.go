//go:build e2e

package docker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestDockerRealGCExactAbsenceE2E(t *testing.T) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires disposable Docker resources")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	owner := fmt.Sprintf("gc-absence-%d", time.Now().UnixNano())
	identity := substrate.OperationIdentity{ActionID: "create", TopologyUID: owner, OwnerUID: owner, ResourceUID: "opaque-resource", Generation: 2}
	labels := operationLabels(identity)
	created, err := s.cli.ContainerCreate(ctx, &container.Config{Image: "alpine:3.22", Labels: labels, Cmd: []string{"sleep", "60"}}, &container.HostConfig{NetworkMode: "none"}, nil, nil, owner)
	require.NoError(t, err)
	defer func() {
		_ = s.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	}()
	netw, err := s.cli.NetworkCreate(ctx, owner, network.CreateOptions{Driver: "bridge", Labels: labels})
	require.NoError(t, err)
	defer func() { _ = s.cli.NetworkRemove(context.Background(), netw.ID) }()
	require.Error(t, s.DeleteManagedExternal(ctx, owner, owner, identity.ResourceUID, 2), "a resolving name is not immutable identity")
	require.Error(t, s.DeleteManagedNetworkExternal(ctx, owner, owner, identity.ResourceUID, 2))
	require.Error(t, s.DeleteManagedExternal(ctx, created.ID, owner, identity.ResourceUID, 1))
	require.Error(t, s.DeleteManagedNetworkExternal(ctx, netw.ID, owner, identity.ResourceUID, 1))
	require.NoError(t, s.DeleteManagedExternal(ctx, created.ID, owner, identity.ResourceUID, 2))
	require.NoError(t, s.DeleteManagedNetworkExternal(ctx, netw.ID, owner, identity.ResourceUID, 2))
	_, err = s.cli.ContainerInspect(ctx, created.ID)
	require.True(t, errdefs.IsNotFound(err), "%v", err)
	_, err = s.cli.NetworkInspect(ctx, netw.ID, network.InspectOptions{})
	require.True(t, errdefs.IsNotFound(err), "%v", err)
	require.NoError(t, s.DeleteManagedExternal(ctx, created.ID, owner, identity.ResourceUID, 2))
	require.NoError(t, s.DeleteManagedNetworkExternal(ctx, netw.ID, owner, identity.ResourceUID, 2))
	t.Log("real GC provider primitives rejected aliases/stale generation and verified container/network absence; not complete GC scheduling acceptance")
}
