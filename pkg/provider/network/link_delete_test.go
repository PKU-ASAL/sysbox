package network

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

type linkDeleteProbe struct {
	deleted, queried      int
	deletion, observation error
	current               netlink.Link
}

func (p *linkDeleteProbe) LinkDel(netlink.Link) error { p.deleted++; return p.deletion }
func (p *linkDeleteProbe) LinkByIndex(int) (netlink.Link, error) {
	p.queried++
	return p.current, p.observation
}

func TestDeleteInspectedLinkRequiresExactAbsenceAfterKernelRace(t *testing.T) {
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Index: 123, Name: "owned", Alias: "verified-owner"}}
	for _, test := range []struct {
		name                  string
		deletion, observation error
		current               netlink.Link
		success               bool
		queries               int
	}{
		{name: "normal completion", success: true},
		{name: "peer vanished", deletion: syscall.ENODEV, observation: syscall.ENODEV, success: true, queries: 1},
		{name: "already absent", deletion: syscall.ENOENT, observation: syscall.ENOENT, success: true, queries: 1},
		{name: "index still present", deletion: syscall.ENODEV, current: link, queries: 1},
		{name: "foreign index reuse", deletion: syscall.ENODEV, current: &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Index: 123, Alias: "foreign"}}, queries: 1},
		{name: "unknown observation", deletion: syscall.ENODEV, observation: syscall.EIO, queries: 1},
		{name: "permission denial", deletion: syscall.EPERM},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &linkDeleteProbe{deletion: test.deletion, observation: test.observation, current: test.current}
			err := deleteInspectedLink(p, link)
			require.Equal(t, test.success, err == nil)
			require.Equal(t, 1, p.deleted)
			require.Equal(t, test.queries, p.queried)
			if test.current != nil {
				require.True(t, driver.IsCategory(err, driver.ErrorInvalidState))
			}
			if test.deletion == syscall.EPERM {
				require.True(t, errors.Is(err, syscall.EPERM))
			}
		})
	}
}

func TestDeleteInspectedLinkRealPeerDisappearance(t *testing.T) {
	if os.Getenv("SYSBOX_TEST_PRIVATE_NETNS") != "1" {
		t.Skip("requires an explicitly isolated NET_ADMIN container")
	}
	h, err := netlink.NewHandle()
	require.NoError(t, err)
	defer h.Close()
	link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "qa-own-veth"}, PeerName: "qa-own-peer"}
	require.NoError(t, h.LinkAdd(link))
	original, err := h.LinkByName("qa-own-veth")
	require.NoError(t, err)
	peer, err := h.LinkByName("qa-own-peer")
	require.NoError(t, err)
	require.NoError(t, h.LinkDel(peer)) // kernel deletes its peer after our identity read
	require.NoError(t, deleteInspectedLink(h, original))
	_, err = h.LinkByIndex(original.Attrs().Index)
	require.True(t, missingLink(err))
}
