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
	dockernet "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

func TestDockerFencedAttachmentResidueE2E(t *testing.T) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires isolated Docker and network privileges")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	owner := fmt.Sprintf("attachment-fence-%d", time.Now().UnixNano())
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: owner, TopologyUID: owner, ResourceUID: "node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	image, _, err := s.cli.ImageInspectWithRaw(ctx, "alpine:3.22")
	require.NoError(t, err)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: owner, Image: substrate.ArtifactHandle{ID: image.ID}, ManagedNetwork: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		err := s.cli.ContainerRemove(context.Background(), h.ID, container.RemoveOptions{Force: true})
		if !errdefs.IsNotFound(err) {
			require.NoError(t, err)
		}
	})
	netw, err := s.cli.NetworkCreate(ctx, owner, dockernet.CreateOptions{Driver: "bridge", Labels: operationLabels(id)})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.cli.NetworkRemove(context.Background(), netw.ID)) })
	require.NoError(t, s.cli.NetworkConnect(ctx, netw.ID, h.ID, nil))
	raw, _ := json.Marshal(attachmentState{Kind: substrate.NICKindDockerNAT, NetworkID: netw.ID})
	require.Error(t, s.HostFenceAttachmentAbsent(ctx, h, "nat", raw), "live container is not absence")
	require.NoError(t, s.cli.ContainerRemove(ctx, h.ID, container.RemoveOptions{Force: true}))
	require.NoError(t, s.HostFenceAttachmentAbsent(ctx, h, "nat", raw))
	// A host veth can survive independently: node absence alone must not pass.
	name := fmt.Sprintf("sbfa%x", time.Now().UnixNano()&0xffffffff)
	alias, err := ownednetwork.AttachmentAlias(id, "lan")
	require.NoError(t, err)
	pair := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name, Alias: alias}, PeerName: name + "p"}
	require.NoError(t, netlink.LinkAdd(pair))
	link, err := netlink.LinkByName(name)
	require.NoError(t, err)
	require.NoError(t, netlink.LinkSetAlias(link, alias))
	t.Cleanup(func() {
		if link, err := netlink.LinkByName(name); err == nil {
			require.NoError(t, netlink.LinkDel(link))
		}
	})
	proof, err := ownednetwork.CaptureAttachmentLink(ctx, "", name, alias)
	require.NoError(t, err)
	raw, _ = json.Marshal(attachmentState{Kind: "veth", Link: &proof})
	require.Error(t, s.HostFenceAttachmentAbsent(ctx, h, "lan", raw), "retained veth is residue")
	require.NoError(t, ownednetwork.DeleteOwnedAttachment(ctx, proof, alias))
	require.NoError(t, s.HostFenceAttachmentAbsent(ctx, h, "lan", raw))
	require.NoError(t, netlink.LinkAdd(pair))
	link, err = netlink.LinkByName(name)
	require.NoError(t, err)
	require.NoError(t, netlink.LinkSetAlias(link, alias))
	require.Error(t, s.HostFenceAttachmentAbsent(ctx, h, "lan", raw), "same-name replacement is not absence")
	t.Log("real container/NAT endpoint deletion and independent veth residue, disappearance and same-name replacement verified")
}
