//go:build e2e

package network

import (
	"context"
	"os"
	"strings"
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

func TestPolicyNamespaceFDReplacementE2E(t *testing.T) {
	if os.Getenv("SYSBOX_NETWORK_E2E") != "1" {
		t.Skip("requires isolated namespace privileges and nft/sysbox-netns tools")
	}
	name := "sbfd-" + uuid.NewString()[:8]
	require.NoError(t, CreateNetns(name))
	t.Cleanup(func() { require.NoError(t, DeleteNetns(name)) })
	old, err := os.Open("/var/run/netns/" + name)
	require.NoError(t, err)
	defer old.Close()
	ctx := context.Background()
	spec := driver.RulesetSpec{Owner: "fd-e2e", Family: driver.FamilyIPv4}
	first, err := ApplyRulesetInNetNS(ctx, old, spec, nil)
	require.NoError(t, err)
	require.NoError(t, DeleteNetns(name))
	require.NoError(t, CreateNetns(name))
	replacement, err := os.Open("/var/run/netns/" + name)
	require.NoError(t, err)
	defer replacement.Close()
	oldInfo, err := old.Stat()
	require.NoError(t, err)
	newInfo, err := replacement.Stat()
	require.NoError(t, err)
	require.False(t, os.SameFile(oldInfo, newInfo))
	spec.DefaultInput = driver.VerdictAccept
	updated, err := ApplyRulesetInNetNS(ctx, old, spec, nil)
	require.NoError(t, err)
	require.NotEqual(t, first.Digest, updated.Digest)
	observed, err := ObserveRulesetInNetNS(ctx, old, spec.Owner)
	require.NoError(t, err)
	require.Equal(t, updated.Digest, observed.Digest)
	_, err = ObserveRulesetInNetNS(ctx, replacement, spec.Owner)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), "%v", err)
	require.NoError(t, DeleteRulesetInNetNS(ctx, old, spec.Owner))
	_, err = ObserveRulesetInNetNS(ctx, old, spec.Owner)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), "%v", err)
	t.Log("real nft Apply/Observe/Delete stayed on held namespace FD after same-name replacement")
}

func TestPolicyAtomicReplacementRollbackE2E(t *testing.T) {
	if os.Getenv("SYSBOX_NETWORK_E2E") != "1" {
		t.Skip("requires isolated namespace privileges and nft/sysbox-netns tools")
	}
	name := "sbat-" + uuid.NewString()[:8]
	require.NoError(t, CreateNetns(name))
	t.Cleanup(func() { require.NoError(t, DeleteNetns(name)) })
	ns, err := os.Open("/var/run/netns/" + name)
	require.NoError(t, err)
	defer ns.Close()
	ctx := context.Background()
	spec := driver.RulesetSpec{Owner: "atomic-e2e", Family: driver.FamilyIPv4, DefaultInput: driver.VerdictDrop}
	original, err := ApplyRulesetInNetNS(ctx, ns, spec, nil)
	require.NoError(t, err)
	before, err := runNFT(ctx, ns, "", "list", "table", "ip", original.Table)
	require.NoError(t, err)
	// Syntactically valid but rejected by the kernel: nft log prefixes are
	// bounded. This exercises rollback after the batch has deleted the table.
	spec.Rules = []driver.PolicyRule{{ID: strings.Repeat("x", 256), Direction: driver.DirectionInput, Protocol: driver.ProtocolTCP, Verdict: driver.VerdictAccept, Log: true}}
	_, err = ApplyRulesetInNetNS(ctx, ns, spec, nil)
	require.Error(t, err, "invalid log prefix must fail the replacement")
	after, err := runNFT(ctx, ns, "", "list", "table", "ip", original.Table)
	require.NoError(t, err)
	require.Equal(t, before, after, "failed replacement must preserve every old chain/rule")
	observed, err := ObserveRulesetInNetNS(ctx, ns, spec.Owner)
	require.NoError(t, err)
	require.Equal(t, original.Digest, observed.Digest)
	spec.Rules = nil
	spec.DefaultInput = driver.VerdictAccept
	updated, err := ApplyRulesetInNetNS(ctx, ns, spec, nil)
	require.NoError(t, err)
	require.NotEqual(t, original.Digest, updated.Digest)
	repeated, err := ApplyRulesetInNetNS(ctx, ns, spec, nil)
	require.NoError(t, err)
	require.Equal(t, updated, repeated)
	require.NoError(t, DeleteRulesetInNetNS(ctx, ns, spec.Owner))
	t.Log("real nft failed replacement preserved complete prior table; create/update/retry/delete passed")
}
