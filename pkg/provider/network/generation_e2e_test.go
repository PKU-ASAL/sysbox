//go:build e2e

package network

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
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

func generationPolicyIdentity() substrate.OperationIdentity {
	return substrate.OperationIdentity{ActionID: "apply", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "policy", Generation: 2}
}

func TestPolicyGenerationFencingE2E(t *testing.T) {
	if os.Getenv("SYSBOX_NETWORK_E2E") != "1" {
		t.Skip("requires isolated namespace and nft privileges")
	}
	name := "sbgen-" + uuid.NewString()[:8]
	require.NoError(t, CreateNetns(name))
	t.Cleanup(func() { require.NoError(t, DeleteNetns(name)) })
	ns, err := os.Open("/var/run/netns/" + name)
	require.NoError(t, err)
	defer ns.Close()
	id := generationPolicyIdentity()
	spec := driver.RulesetSpec{Owner: "generation-e2e", Family: driver.FamilyIPv4, Rules: []driver.PolicyRule{{ID: "quoted-{", Direction: driver.DirectionInput, Protocol: driver.ProtocolTCP, Verdict: driver.VerdictAccept, Log: true}}}
	ctx := context.Background()
	first, err := ApplyManagedRuleset(ctx, ns, id, spec, nil)
	require.NoError(t, err)
	found, err := DiscoverManagedRuleset(ctx, ns, id, spec, nil)
	require.NoError(t, err)
	require.Equal(t, first, found)
	drifted := spec
	drifted.DefaultInput = driver.VerdictAccept
	_, err = DiscoverManagedRuleset(ctx, ns, id, drifted, nil)
	require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "discovery must not adopt a different desired digest: %v", err)
	before, err := runNFT(ctx, ns, "", "list", "table", "ip", first.Table)
	require.NoError(t, err)
	old := id
	old.Generation--
	_, err = ApplyManagedRuleset(ctx, ns, old, spec, nil)
	require.Error(t, err)
	require.Error(t, DeleteManagedRuleset(ctx, ns, old, spec.Owner))
	changed := spec
	changed.DefaultInput = driver.VerdictAccept
	_, err = ApplyManagedRuleset(ctx, ns, id, changed, nil)
	require.Error(t, err)
	after, err := runNFT(ctx, ns, "", "list", "table", "ip", first.Table)
	require.NoError(t, err)
	require.Equal(t, before, after)
	// Recovery in another OS process reads the kernel identity and retries
	// the same digest without rebuilding the table.
	cmd := exec.Command(os.Args[0], "-test.run=^TestPolicyGenerationProcessHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "SYSBOX_POLICY_TEST_NS="+name)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	id.Generation++
	var results [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			desired := spec
			if i == 1 {
				desired = changed
			}
			_, results[i] = ApplyManagedRuleset(ctx, ns, id, desired, nil)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else {
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
		}
	}
	require.Equal(t, 1, winners, "one immutable digest must win the generation")
	_, err = ObserveManagedRuleset(ctx, ns, id, spec.Owner)
	require.NoError(t, err)
	require.NoError(t, DeleteManagedRuleset(ctx, ns, id, spec.Owner))
	require.NoError(t, DeleteManagedRuleset(ctx, ns, id, spec.Owner))
	_, err = ApplyManagedRuleset(ctx, ns, id, spec, nil)
	require.Error(t, err, "deleted generation must not resurrect")
	cmd = exec.Command(os.Args[0], "-test.run=^TestPolicyGenerationProcessHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "SYSBOX_POLICY_TEST_NS="+name, "SYSBOX_POLICY_TEST_RETIRED=1")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	id.Generation++
	_, err = ApplyManagedRuleset(ctx, ns, id, spec, nil)
	require.NoError(t, err)
	// Model a crash after physical removal and before the final evidence write.
	store := policyEvidenceStore{root: "/var/lib/sysbox/policy-evidence"}
	require.NoError(t, store.save(ns, spec.Owner, policyEvidence{Version: 1, Identity: id, Phase: "deleting"}))
	require.NoError(t, DeleteRulesetInNetNS(ctx, ns, spec.Owner))
	_, err = ApplyManagedRuleset(ctx, ns, id, spec, nil)
	require.Error(t, err)
	require.NoError(t, DeleteManagedRuleset(ctx, ns, id, spec.Owner))
	retired, err := store.load(ns, spec.Owner)
	require.NoError(t, err)
	require.Equal(t, "deleted", retired.Phase)
	t.Log("stale Apply/Delete rejected; independent-process retry and concurrent generation arbitration passed")
}

func TestPolicyGenerationProcessHelper(t *testing.T) {
	name := os.Getenv("SYSBOX_POLICY_TEST_NS")
	if name == "" {
		t.Skip("subprocess helper")
	}
	ns, err := os.Open("/var/run/netns/" + name)
	require.NoError(t, err)
	defer ns.Close()
	id := generationPolicyIdentity()
	spec := driver.RulesetSpec{Owner: "generation-e2e", Family: driver.FamilyIPv4, Rules: []driver.PolicyRule{{ID: "quoted-{", Direction: driver.DirectionInput, Protocol: driver.ProtocolTCP, Verdict: driver.VerdictAccept, Log: true}}}
	if os.Getenv("SYSBOX_POLICY_TEST_RETIRED") == "1" {
		_, err := ApplyManagedRuleset(context.Background(), ns, id, spec, nil)
		require.Error(t, err, "new process must not resurrect old policy")
		return
	}
	observed, err := ObserveManagedRuleset(context.Background(), ns, id, spec.Owner)
	require.NoError(t, err)
	retried, err := ApplyManagedRuleset(context.Background(), ns, id, spec, nil)
	require.NoError(t, err)
	require.Equal(t, observed, retried)
}

func TestOwnedAttachmentGenerationE2E(t *testing.T) {
	if os.Getenv("SYSBOX_NETWORK_E2E") != "1" {
		t.Skip("requires isolated namespace privileges")
	}
	name := "sblink-" + uuid.NewString()[:8]
	require.NoError(t, CreateNetns(name))
	t.Cleanup(func() { require.NoError(t, DeleteNetns(name)) })
	require.NoError(t, CreateBridge(BridgeConfig{NetnsName: name, BridgeName: "br0", CIDR: "10.253.10.1/24"}))
	id := substrate.OperationIdentity{ActionID: "attach", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "node", Generation: 2}
	alias, err := AttachmentAlias(id, "nic")
	require.NoError(t, err)
	spec := VethSpec{HostEnd: "owned", GuestEnd: "peer", NetnsName: name, BridgeName: "br0", OwnerAlias: alias}
	_, err = CreateVethPair(spec)
	require.NoError(t, err)
	ctx := context.Background()
	proof, err := CaptureAttachmentLink(ctx, name, "owned", alias)
	require.NoError(t, err)
	require.NoError(t, ObserveOwnedAttachment(ctx, proof, alias))
	old := id
	old.Generation--
	oldAlias, err := AttachmentAlias(old, "nic")
	require.NoError(t, err)
	stale := spec
	stale.OwnerAlias = oldAlias
	_, err = CreateVethPair(stale)
	require.Error(t, err)
	require.Error(t, DeleteOwnedAttachment(ctx, proof, oldAlias))
	nsHandle, err := (Driver{}).openNetlink(name)
	require.NoError(t, err)
	link, err := nsHandle.LinkByName("owned")
	require.NoError(t, err)
	require.NoError(t, nsHandle.LinkDel(link))
	nsHandle.Close()
	_, err = CreateVethPair(spec)
	require.NoError(t, err)
	require.Error(t, DeleteOwnedAttachment(ctx, proof, alias), "old ifindex cannot delete same-name replacement")
	current, err := CaptureAttachmentLink(ctx, name, "owned", alias)
	require.NoError(t, err)
	require.NoError(t, DeleteOwnedAttachment(ctx, current, alias))
	require.NoError(t, DeleteOwnedAttachment(ctx, current, alias))
	_, err = CreateVethPair(spec)
	require.NoError(t, err)
	current, err = CaptureAttachmentLink(ctx, name, "owned", alias)
	require.NoError(t, err)
	require.NoError(t, DeleteNetns(name))
	require.NoError(t, CreateNetns(name))
	require.Error(t, DeleteOwnedAttachment(ctx, current, alias), "replacement namespace is not the owned incarnation")
	t.Log("real veth stale generation, replaced ifindex and replaced namespace rejected; exact proof deletion idempotent")
}

func TestNetworkBridgeReplacementRejectedE2E(t *testing.T) {
	if os.Getenv("SYSBOX_NETWORK_E2E") != "1" {
		t.Skip("requires isolated network privileges")
	}
	for _, rootBridge := range []bool{false, true} {
		t.Run(fmt.Sprint(rootBridge), func(t *testing.T) {
			suffix := uuid.NewString()[:8]
			spec := driver.IsolatedNetworkSpec{Name: "sbrep-" + suffix, Bridge: "br0", CIDR: "10.251.0.1/24", RootBridge: "sbb" + suffix, RootEnd: "sbr" + suffix, NamespaceEnd: "sbn" + suffix}
			d := Driver{ownershipRoot: t.TempDir()}
			id := substrate.OperationIdentity{OwnerUID: "owner", TopologyUID: "topology", ResourceUID: suffix, Generation: 2, ActionID: "create"}
			ctx := substrate.WithOperationIdentity(context.Background(), id)
			require.NoError(t, d.CreateIsolated(ctx, spec))
			t.Cleanup(func() { require.NoError(t, DeleteRootBridgeProxy(spec)); require.NoError(t, DeleteNetns(spec.Name)) })
			namespace, name := spec.Name, spec.Bridge
			if rootBridge {
				namespace, name = "", spec.RootBridge
			}
			h, err := d.openNetlink(namespace)
			require.NoError(t, err)
			defer h.Close()
			old, err := h.LinkByName(name)
			require.NoError(t, err)
			require.NoError(t, h.LinkDel(old))
			index, err := newAttachmentIndex()
			require.NoError(t, err)
			fake := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: name, Index: index}}
			require.NoError(t, h.LinkAdd(fake))
			require.NoError(t, h.LinkSetAlias(fake, networkAlias(id)))
			_, err = d.ObserveIsolated(ctx, spec)
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
			require.Error(t, d.DeleteIsolated(ctx, spec))
			require.Error(t, d.CreateIsolated(ctx, spec))
			preserved, err := h.LinkByName(name)
			require.NoError(t, err)
			require.Equal(t, index, preserved.Attrs().Index)
		})
	}
}
