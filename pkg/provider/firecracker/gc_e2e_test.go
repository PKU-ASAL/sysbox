//go:build e2e

package firecracker

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerRealGCExactOwnerAndSurvivorE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	create := func(uid string) (substrate.NodeHandle, context.Context) {
		op := substrate.OperationIdentity{ActionID: "create-" + uid, OwnerUID: "gc-owner", TopologyUID: "gc-owner", ResourceUID: uid, Generation: 1}
		scoped := substrate.WithOperationIdentity(ctx, op)
		h, err := s.CreateNode(scoped, substrate.NodeSpec{Name: uid, Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel}})
		require.NoError(t, err)
		cleanupRealVM(t, s, h)
		require.NoError(t, s.StartNode(scoped, h))
		return h, scoped
	}
	target, targetCtx := create("gc-target")
	survivor, survivorCtx := create("gc-survivor")
	require.Error(t, s.DeleteManagedExternal(targetCtx, target.ID, "gc-owner", "gc-target", 2))
	obs, err := s.ObserveNode(targetCtx, target)
	require.NoError(t, err)
	require.True(t, obs.Exists)
	require.NoError(t, s.DeleteManagedExternal(targetCtx, target.ID, "gc-owner", "gc-target", 1))
	obs, err = s.ObserveNode(targetCtx, target)
	require.NoError(t, err)
	require.False(t, obs.Exists)
	obs, err = s.ObserveNode(survivorCtx, survivor)
	require.NoError(t, err)
	require.True(t, obs.Exists)
	require.True(t, obs.Running)
}
