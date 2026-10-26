package docker

import (
	"context"
	"github.com/oslab/sysbox/pkg/driver"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachmentDiscoveryRejectsMissingIdentity(t *testing.T) {
	var s any = &Substrate{}
	discovery, ok := s.(driver.ManagedAttachmentDiscovery)
	require.True(t, ok)
	_, err := discovery.DiscoverManagedAttachments(context.Background(), substrate.NodeHandle{}, nil)
	require.Error(t, err)
}

func TestDiscoveredKernelLinksRejectRawAndMissingEndpoints(t *testing.T) {
	mac, _ := net.ParseMAC("02:00:00:00:00:01")
	lo := &netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: "lo", Index: 1}}
	endpoint := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "eth0", Index: 2, HardwareAddr: mac}}
	for _, mode := range []string{"exact", "raw", "missing", "wrong-kind"} {
		t.Run(mode, func(t *testing.T) {
			links := []netlink.Link{lo, endpoint}
			switch mode {
			case "raw":
				links = append(links, &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "untracked", Index: 3}})
			case "missing":
				links = links[:1]
			case "wrong-kind":
				links[1] = &netlink.Dummy{LinkAttrs: endpoint.LinkAttrs}
			}
			err := validateDiscoveredKernelLinks(links, map[string]bool{mac.String(): true})
			if mode == "exact" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestDiscoveryNamespaceBindingRejectsSubstitution(t *testing.T) {
	dir := t.TempDir()
	sandbox := filepath.Join(dir, "sandbox")
	process := filepath.Join(dir, "process")
	require.NoError(t, os.WriteFile(sandbox, []byte("namespace"), 0600))
	require.NoError(t, os.Link(sandbox, process))
	fd, err := os.Open(sandbox)
	require.NoError(t, err)
	defer fd.Close()
	require.NoError(t, validateDiscoveryNamespaceBinding(sandbox, process, int(fd.Fd())))
	require.NoError(t, os.Remove(sandbox))
	require.NoError(t, os.WriteFile(sandbox, []byte("replacement"), 0600))
	require.Error(t, validateDiscoveryNamespaceBinding(sandbox, process, int(fd.Fd())), "unchanged pathname cannot replace pinned namespace")
	require.NoError(t, os.Remove(sandbox))
	require.NoError(t, os.Link(process, sandbox))
	require.NoError(t, os.Remove(process))
	require.NoError(t, os.WriteFile(process, []byte("other pid namespace"), 0600))
	require.Error(t, validateDiscoveryNamespaceBinding(sandbox, process, int(fd.Fd())), "sandbox must match current container namespace")
}

func TestRawVethDiscoveryRequiresExactOwnedPeer(t *testing.T) {
	proof := ownednetwork.AttachmentLink{Index: 123, Alias: "owned", Kind: "veth"}
	mac, _ := net.ParseMAC("02:00:00:00:00:02")
	for _, mode := range []string{"exact", "alias", "peer", "name", "kind", "mac"} {
		t.Run(mode, func(t *testing.T) {
			attrs := netlink.LinkAttrs{Name: "eth1", Index: 456, ParentIndex: 123, Alias: "owned", HardwareAddr: mac}
			var link netlink.Link = &netlink.Veth{LinkAttrs: attrs}
			switch mode {
			case "alias":
				link.Attrs().Alias = "foreign"
			case "peer":
				link.Attrs().ParentIndex++
			case "name":
				link.Attrs().Name = "other"
			case "kind":
				link = &netlink.Dummy{LinkAttrs: attrs}
			case "mac":
				link.Attrs().HardwareAddr = nil
			}
			err := validateDiscoveredRawPeer(link, proof, "eth1", mac.String())
			if mode == "exact" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
