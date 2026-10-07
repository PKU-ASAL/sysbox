//go:build e2e

package docker

import (
	"context"
	"fmt"
	dockernet "github.com/docker/docker/api/types/network"
	"github.com/oslab/sysbox/pkg/driver"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"os"
	"testing"
	"time"
)

func TestDockerManagedBridgeTargetE2E(t *testing.T) {
	if os.Getenv("SYSBOX_MANAGED_BRIDGE_E2E") != "1" {
		t.Skip("requires Docker socket, host netns, NET_ADMIN and /dev/net/tun")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Network: s}))
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: fmt.Sprintf("mixed-e2e-%d", time.Now().UnixNano()), TopologyUID: "test", ResourceUID: "network", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	defer func() {
		existing, err := s.cli.NetworkInspect(context.Background(), "sysbox-nat-"+id.OwnerUID, dockernet.InspectOptions{})
		if err == nil && existing.Labels["sysbox.owner_uid"] == id.OwnerUID {
			_ = s.cli.NetworkRemove(context.Background(), existing.ID)
		}
	}()
	info, err := s.CreateManagedNetwork(ctx, substrate.ManagedNetworkSpec{Name: id.OwnerUID, CIDR: fmt.Sprintf("10.229.%d.0/24", time.Now().UnixNano()%200+20), NAT: true, ExposeBridge: true})
	require.NoError(t, err)
	defer func() { _ = s.cli.NetworkRemove(context.Background(), info.ID) }()
	require.NotNil(t, info.BridgeTarget)
	proof := *info.BridgeTarget
	require.Equal(t, ownednetwork.ManagedBridgeName(id), proof.Bridge)
	target := ownednetwork.BridgeTarget{Identity: id, Bridge: proof.Bridge, Managed: &proof}
	bridge, err := ownednetwork.OpenOwnedBridge(ctx, target)
	require.NoError(t, err)
	tap := fmt.Sprintf("te%x", time.Now().UnixNano()&0xffffff)
	alias, err := ownednetwork.AttachmentAlias(id, tap)
	require.NoError(t, err)
	require.NoError(t, bridge.AttachTap(ctx, tap, alias))
	attachment, err := bridge.CaptureAttachment(ctx, tap, alias)
	require.NoError(t, err)
	require.NoError(t, bridge.ObserveAttachment(ctx, attachment, alias))
	bridge.Close()
	require.NoError(t, ownednetwork.DeleteOwnedAttachment(ctx, attachment, alias))
	old := target
	old.Identity.Generation++
	_, err = ownednetwork.OpenOwnedBridge(ctx, old)
	require.Error(t, err)
	actual, err := netlink.LinkByIndex(proof.Index)
	require.NoError(t, err)
	require.NoError(t, netlink.LinkDel(actual))
	replacement := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: proof.Bridge, Alias: proof.Alias}}
	require.NoError(t, netlink.LinkAdd(replacement))
	defer netlink.LinkDel(replacement)
	replacementLink, err := netlink.LinkByName(proof.Bridge)
	require.NoError(t, err)
	require.NotEqual(t, proof.Index, replacementLink.Attrs().Index)
	_, err = ownednetwork.OpenOwnedBridge(ctx, target)
	require.Error(t, err)
	require.Error(t, s.RemoveManagedNetwork(ctx, info.ID))
	_, err = netlink.LinkByIndex(replacementLink.Attrs().Index)
	require.NoError(t, err, "replacement must survive rejected delete")
	t.Log("explicit Docker bridge proof attached a real TAP; stale generation and same-name replacement rejected")
}
