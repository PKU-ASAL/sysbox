//go:build e2e

package network

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestIsolatedNetworkGenerationRecoveryE2E(t *testing.T) {
	if os.Getenv("SYSBOX_NETWORK_E2E") != "1" {
		t.Skip("set SYSBOX_NETWORK_E2E=1 with namespace/network administration privileges")
	}
	suffix := uuid.NewString()[:8]
	spec := driver.IsolatedNetworkSpec{Name: "sbf-" + suffix, Bridge: "br0", CIDR: "10.253.0.1/24", RootBridge: "sbb" + suffix, RootEnd: "sbr" + suffix, NamespaceEnd: "sbn" + suffix}
	d := Driver{ownershipRoot: t.TempDir()}
	id := substrate.OperationIdentity{OwnerUID: uuid.NewString(), TopologyUID: uuid.NewString(), ResourceUID: uuid.NewString(), Generation: 1, ActionID: "create"}
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	t.Cleanup(func() {
		// Cleanup is scoped to the current identity and test-created names.
		// Run via the disposable-container Make target so even an incomplete
		// creation that cannot prove ownership is contained by container teardown.
		require.NoError(t, d.DeleteIsolated(substrate.WithOperationIdentity(context.Background(), id), spec))
	})
	require.NoError(t, d.CreateIsolated(ctx, spec))
	require.NoError(t, d.DeleteIsolated(ctx, spec))
	id.Generation++
	current := substrate.WithOperationIdentity(context.Background(), id)
	require.NoError(t, d.CreateIsolated(current, spec))
	before, err := os.Stat("/var/run/netns/" + spec.Name)
	require.NoError(t, err)
	for _, action := range []func(context.Context, driver.IsolatedNetworkSpec) error{d.CreateIsolated, d.DeleteIsolated} {
		err := action(ctx, spec)
		require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
	}
	after, err := os.Stat("/var/run/netns/" + spec.Name)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	// No cached driver state is needed to verify the namespace inode and aliases.
	reopened := Driver{ownershipRoot: d.ownershipRoot}
	observed, err := reopened.ObserveIsolated(current, spec)
	require.NoError(t, err)
	require.True(t, observed.Healthy, observed.Reason)
	// Simulate interruption after physical deletion, before retiring the anchor.
	require.NoError(t, DeleteRootBridgeProxy(spec))
	require.NoError(t, DeleteNetns(spec.Name))
	require.FileExists(t, d.ownershipPath(spec.Name))
	require.Error(t, reopened.CreateIsolated(ctx, spec))
	require.Error(t, reopened.DeleteIsolated(ctx, spec))
	require.NoError(t, reopened.DeleteIsolated(current, spec))
	require.NoFileExists(t, d.ownershipPath(spec.Name))
	t.Log("real namespace/bridge/veth generation replacement, stale rejection, and interrupted-delete recovery passed")
}
