//go:build e2e

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/oslab/sysbox/pkg/driver"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

func TestDockerRawAttachmentDiscoveryE2E(t *testing.T) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires enrolled disposable Docker host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	name := fmt.Sprintf("raw-discovery-%d", time.Now().UnixNano())
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: name, TopologyUID: name, ResourceUID: "node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	netID := id
	netID.ResourceUID = "network"
	netCtx := substrate.WithOperationIdentity(ctx, netID)
	nsName := fmt.Sprintf("sbrd%x", time.Now().UnixNano()&0xffffffff)
	spec := driver.IsolatedNetworkSpec{Name: nsName, Bridge: "br0", CIDR: "10.46.0.1/24"}
	require.NoError(t, (ownednetwork.Driver{}).CreateIsolated(netCtx, spec))
	t.Cleanup(func() {
		require.NoError(t, (ownednetwork.Driver{}).DeleteIsolated(substrate.WithOperationIdentity(context.Background(), netID), spec))
	})
	image, _, err := s.cli.ImageInspectWithRaw(ctx, "alpine:3.22")
	require.NoError(t, err)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: name, Image: substrate.ArtifactHandle{ID: image.ID}})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, s.cli.ContainerRemove(context.Background(), h.ID, container.RemoveOptions{Force: true}))
	})
	require.NoError(t, s.cli.ContainerStart(ctx, h.ID, container.StartOptions{}))
	request := driver.AttachmentRequest{Name: "lan", NetworkIdentity: netID, MAC: "02:00:00:00:46:02", IPPrefixes: []string{"10.46.0.2/24"}, NetworkState: json.RawMessage(`{"netns":"` + nsName + `","bridge":"br0"}`)}
	attached, err := s.Attach(ctx, h, request)
	require.NoError(t, err)
	// Discard response state and rediscover only from original generation and request.
	cold, err := s.DiscoverManagedNode(ctx, id, name)
	require.NoError(t, err)
	found, err := s.DiscoverManagedAttachments(ctx, cold, []driver.AttachmentRequest{request})
	require.NoError(t, err)
	require.JSONEq(t, string(attached.State), string(found[request.Name].State))
	_, err = s.DiscoverManagedAttachments(ctx, cold, nil)
	require.Error(t, err, "empty desired must not hide raw link")
	wrong := request
	wrong.NetworkIdentity.Generation++
	_, err = s.DiscoverManagedAttachments(ctx, cold, []driver.AttachmentRequest{wrong})
	require.Error(t, err)
	inspect, err := s.cli.ContainerInspect(ctx, h.ID)
	require.NoError(t, err)
	ns, err := netns.GetFromPid(inspect.State.Pid)
	require.NoError(t, err)
	defer ns.Close()
	links, err := netlink.NewHandleAt(ns)
	require.NoError(t, err)
	defer links.Close()
	peer, err := links.LinkByName(dockerGuestName(request.Name))
	require.NoError(t, err)
	original := peer.Attrs().Alias
	require.NoError(t, links.LinkSetAlias(peer, "foreign"))
	_, err = s.DiscoverManagedAttachments(ctx, cold, []driver.AttachmentRequest{request})
	require.Error(t, err)
	require.NoError(t, links.LinkSetAlias(peer, original))
	again, err := s.DiscoverManagedAttachments(ctx, cold, []driver.AttachmentRequest{request})
	require.NoError(t, err)
	require.Equal(t, found, again)
	hostNS, err := netns.GetFromName(nsName)
	require.NoError(t, err)
	defer hostNS.Close()
	hostLinks, err := netlink.NewHandleAt(hostNS)
	require.NoError(t, err)
	defer hostLinks.Close()
	extra := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "extra-owned"}, PeerName: "extra-peer"}
	require.NoError(t, hostLinks.LinkAdd(extra))
	retained, err := hostLinks.LinkByName("extra-owned")
	require.NoError(t, err)
	require.NoError(t, hostLinks.LinkSetAlias(retained, original))
	_, err = s.DiscoverManagedAttachments(ctx, cold, []driver.AttachmentRequest{request})
	require.Error(t, err, "detached host residue cannot be omitted from inventory")
	require.NoError(t, hostLinks.LinkDel(retained))
	_, err = s.DiscoverManagedAttachments(ctx, cold, []driver.AttachmentRequest{request})
	require.NoError(t, err)
	t.Log("real raw veth: cold identity/attachment discovery; omitted, foreign network generation and substituted peer rejected; no repair or replay")
}
