package network

import (
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"testing"
)

func TestVethDiscoveryRejectsSameIndexInWrongNamespace(t *testing.T) {
	host := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Index: 12, ParentIndex: 23, NetNsID: 4}}
	peer := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Index: 23, ParentIndex: 12, NetNsID: 7}}
	require.True(t, sameVethPeerNamespaces(host, peer, 4, 7))
	require.False(t, sameVethPeerNamespaces(host, peer, 4, 8), "same ifindex in different host namespace is not the peer")
	require.False(t, sameVethPeerNamespaces(host, peer, 5, 7))
	require.False(t, sameVethPeerNamespaces(host, peer, -1, 7))
	host.ParentIndex = 24
	require.False(t, sameVethPeerNamespaces(host, peer, 4, 7), "both peer directions must match")
}
