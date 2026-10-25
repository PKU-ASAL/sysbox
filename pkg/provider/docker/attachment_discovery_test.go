package docker

import (
	"context"
	"github.com/oslab/sysbox/pkg/driver"
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
