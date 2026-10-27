//go:build e2e

package network

import (
	"context"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"os"
	"testing"
)

func TestAttachmentTargetFencingE2E(t *testing.T) {
	if os.Getenv("SYSBOX_NETWORK_E2E") != "1" {
		t.Skip("requires isolated network privileges")
	}
	d := Driver{}
	id := substrate.OperationIdentity{OwnerUID: "owner", TopologyUID: "topology", ResourceUID: uuid.NewString(), Generation: 2, ActionID: "create"}
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	spec := driver.IsolatedNetworkSpec{Name: "sbtarget-" + uuid.NewString()[:8], Bridge: "br0", CIDR: "10.252.0.1/24"}
	require.NoError(t, d.CreateIsolated(ctx, spec))
	t.Cleanup(func() { require.NoError(t, DeleteNetns(spec.Name)); require.NoError(t, d.removeOwnership(spec.Name)) })
	target := BridgeTarget{Identity: id, NetworkNamespace: spec.Name, Bridge: spec.Bridge}
	stale := target
	stale.Identity.Generation--
	_, err := OpenOwnedBridge(ctx, stale)
	require.Error(t, err)
	_, err = CreateVethPair(ctx, stale, VethSpec{HostEnd: "owned", GuestEnd: "peer", OwnerAlias: "node-proof"})
	require.Error(t, err)
	b, err := OpenOwnedBridge(ctx, target)
	require.NoError(t, err)
	pair, err := b.CreateVethPair(ctx, VethSpec{HostEnd: "owned", GuestEnd: "peer", OwnerAlias: "node-proof"})
	require.NoError(t, err)
	destination := "sbguest-" + uuid.NewString()[:8]
	require.NoError(t, CreateNetns(destination))
	t.Cleanup(func() { require.NoError(t, DeleteNetns(destination)) })
	ns, destHandle, err := openAttachmentNamespace(destination)
	require.NoError(t, err)
	defer ns.Close()
	defer destHandle.Close()
	wrongPair := pair
	wrongPair.HostIndex++
	require.Error(t, b.MoveVethPeer(ctx, wrongPair, int(ns.Fd())))
	require.NoError(t, b.MoveVethPeer(ctx, pair, int(ns.Fd())))
	peer, err := destHandle.LinkByName(pair.GuestEnd)
	require.NoError(t, err)
	require.Equal(t, pair.HostIndex, peer.Attrs().ParentIndex)
	_, err = b.CreateVethPair(ctx, VethSpec{HostEnd: "owned", GuestEnd: "peer", OwnerAlias: "node-proof"})
	require.ErrorContains(t, err, "refusing destructive retry")
	proof, err := b.CaptureAttachment(ctx, "owned", "node-proof")
	require.NoError(t, err)
	require.NoError(t, DeleteOwnedAttachment(ctx, proof, "node-proof"))
	b.Close()
	h, err := d.openNetlink(spec.Name)
	require.NoError(t, err)
	defer h.Close()
	bridge, err := h.LinkByName(spec.Bridge)
	require.NoError(t, err)
	original := bridge.Attrs().Index
	require.NoError(t, h.LinkDel(bridge))
	replacement := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: spec.Bridge, Index: original + 1}}
	require.NoError(t, h.LinkAdd(replacement))
	replacementLink, err := h.LinkByName(spec.Bridge)
	require.NoError(t, err)
	require.NoError(t, h.LinkSetAlias(replacementLink, networkAlias(id)))
	_, err = OpenOwnedBridge(ctx, target)
	require.Error(t, err, "same name and alias are not an incarnation proof")
	_, err = CreateVethPair(ctx, target, VethSpec{HostEnd: "owned", GuestEnd: "peer", OwnerAlias: "node-proof"})
	require.Error(t, err)
	_, err = h.LinkByName("owned")
	require.True(t, missingLink(err))
}

func TestRootAttachmentRejectsReplacedNetworkNamespaceE2E(t *testing.T) {
	if os.Getenv("SYSBOX_NETWORK_E2E") != "1" {
		t.Skip("requires isolated network privileges")
	}
	suffix := uuid.NewString()[:8]
	d := Driver{}
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: suffix, Generation: 1}
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	spec := driver.IsolatedNetworkSpec{Name: "sbroot-" + suffix, Bridge: "br0", CIDR: "10.251.0.1/24", RootBridge: "br" + suffix, RootEnd: "vr" + suffix, NamespaceEnd: "vn" + suffix}
	require.NoError(t, d.CreateIsolated(ctx, spec))
	t.Cleanup(func() {
		require.NoError(t, DeleteRootBridgeProxy(spec))
		require.NoError(t, DeleteNetns(spec.Name))
		require.NoError(t, d.removeOwnership(spec.Name))
	})
	target := BridgeTarget{Identity: id, NetworkNamespace: spec.Name, Bridge: spec.RootBridge, Root: true}
	b, err := OpenOwnedBridge(ctx, target)
	require.NoError(t, err)
	b.Close()
	require.NoError(t, DeleteNetns(spec.Name))
	require.NoError(t, CreateNetns(spec.Name))
	_, err = OpenOwnedBridge(ctx, target)
	require.ErrorContains(t, err, "namespace incarnation changed")
}
