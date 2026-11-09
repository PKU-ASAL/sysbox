package network

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/vishvananda/netlink"
)

// Each link is assigned an index before creation. The write-ahead proof is
// persisted before the effect, so interruption never grants ownership by name.
func (d Driver) createOwnedNetworkLinks(ctx context.Context, m *networkOwnership) error {
	spec := m.Spec
	ns, nh, err := openAttachmentNamespace(spec.Name)
	if err != nil {
		return err
	}
	defer ns.Close()
	defer nh.Close()
	if err := checkNamespaceFile(ns, m.NamespaceDevice, m.NamespaceInode); err != nil {
		return err
	}
	root, rh, err := openAttachmentNamespace("")
	if err != nil {
		return err
	}
	defer root.Close()
	defer rh.Close()
	alias := networkAlias(m.Identity)
	bridge, err := d.createRecordedLink(ctx, m, ns, nh, spec.Name, spec.Bridge, "bridge", alias, "")
	if err != nil {
		return err
	}
	addr, err := netlink.ParseAddr(spec.CIDR)
	if err != nil {
		return err
	}
	if err := nh.AddrAdd(bridge, addr); err != nil {
		return err
	}
	if err := nh.LinkSetUp(bridge); err != nil {
		return err
	}
	if spec.RootBridge == "" {
		return nil
	}
	rootBridge, err := d.createRecordedLink(ctx, m, root, rh, "", spec.RootBridge, "bridge", alias, "")
	if err != nil {
		return err
	}
	if err := rh.LinkSetUp(rootBridge); err != nil {
		return err
	}
	rootEnd, err := d.createRecordedLink(ctx, m, root, rh, "", spec.RootEnd, "veth", alias, spec.NamespaceEnd)
	if err != nil {
		return err
	}
	// The peer must belong to the link just created, not merely share its name.
	peer, err := rh.LinkByName(spec.NamespaceEnd)
	if err != nil {
		return err
	}
	if peer.Type() != "veth" || peer.Attrs().ParentIndex != rootEnd.Attrs().Index {
		return fmt.Errorf("proxy peer identity mismatch")
	}
	if err := rh.LinkSetAlias(peer, alias); err != nil {
		return err
	}
	peerProof, err := networkLinkProof(ns, spec.Name, peer, alias)
	if err != nil {
		return err
	}
	m.Links = append(m.Links, peerProof)
	if err := d.writeOwnership(spec.Name, *m); err != nil {
		return err
	}
	if err := rh.LinkSetNsFd(peer, int(ns.Fd())); err != nil {
		return err
	}
	peer, err = nh.LinkByName(spec.NamespaceEnd)
	if err != nil {
		return err
	}
	if peer.Attrs().Index != peerProof.Index || peer.Attrs().Alias != alias || peer.Attrs().ParentIndex != rootEnd.Attrs().Index {
		return fmt.Errorf("moved proxy peer identity mismatch")
	}
	if err := nh.LinkSetMaster(peer, bridge); err != nil {
		return err
	}
	if err := nh.LinkSetUp(peer); err != nil {
		return err
	}
	if err := rh.LinkSetMaster(rootEnd, rootBridge); err != nil {
		return err
	}
	if err := rh.LinkSetUp(rootEnd); err != nil {
		return err
	}
	if hasRootIngress(spec) {
		address, err := netlink.ParseAddr(spec.RootAddress)
		if err != nil {
			return err
		}
		if err := rh.AddrAdd(rootBridge, address); err != nil {
			return err
		}
		_, dst, err := net.ParseCIDR(spec.CIDR)
		if err != nil {
			return err
		}
		if err := rh.RouteReplace(&netlink.Route{LinkIndex: rootBridge.Attrs().Index, Dst: dst, Scope: netlink.SCOPE_LINK}); err != nil {
			return err
		}
		for _, route := range spec.RootRoutes {
			_, dst, err := net.ParseCIDR(route.Destination)
			if err != nil {
				return err
			}
			if err := rh.RouteReplace(&netlink.Route{LinkIndex: rootBridge.Attrs().Index, Dst: dst, Gw: net.ParseIP(route.Via)}); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (d Driver) createRecordedLink(ctx context.Context, m *networkOwnership, ns *os.File, h *netlink.Handle, namespace, name, kind, alias, peer string) (netlink.Link, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := h.LinkByName(name); err == nil {
		return nil, fmt.Errorf("refusing existing network link %s", name)
	} else if !missingLink(err) {
		return nil, err
	}
	index, err := newAttachmentIndex()
	if err != nil {
		return nil, err
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	attrs.Index = index
	var link netlink.Link = &netlink.Bridge{LinkAttrs: attrs}
	if kind == "veth" {
		link = &netlink.Veth{LinkAttrs: attrs, PeerName: peer}
	}
	proof, err := networkLinkProof(ns, namespace, link, alias)
	if err != nil {
		return nil, err
	}
	m.Links = append(m.Links, proof)
	if err := d.writeOwnership(m.Spec.Name, *m); err != nil {
		return nil, err
	}
	if err := h.LinkAdd(link); err != nil {
		return nil, err
	}
	actual, err := h.LinkByIndex(index)
	if err != nil {
		return nil, err
	}
	if actual.Attrs().Name != name || actual.Type() != kind {
		return nil, fmt.Errorf("created network link identity changed")
	}
	if err := h.LinkSetAlias(actual, alias); err != nil {
		return nil, err
	}
	return actual, nil
}

func networkLinkProof(ns *os.File, namespace string, link netlink.Link, alias string) (AttachmentLink, error) {
	info, err := ns.Stat()
	if err != nil {
		return AttachmentLink{}, err
	}
	stat := info.Sys().(*syscall.Stat_t)
	return AttachmentLink{Version: 1, Namespace: namespace, Device: uint64(stat.Dev), Inode: stat.Ino, Name: link.Attrs().Name, Index: link.Attrs().Index, Kind: link.Type(), Alias: alias}, nil
}

func validateNetworkLinkProofs(m networkOwnership) error {
	expected := map[string]string{m.Spec.Name + "/" + m.Spec.Bridge: "bridge"}
	if m.Spec.RootBridge != "" {
		expected["/"+m.Spec.RootBridge] = "bridge"
		expected["/"+m.Spec.RootEnd] = "veth"
		expected[m.Spec.Name+"/"+m.Spec.NamespaceEnd] = "veth"
	}
	seen := map[string]bool{}
	for _, p := range m.Links {
		key := p.Namespace + "/" + p.Name
		if p.Version != 1 || p.Index <= 0 || p.Inode == 0 || p.Alias != networkAlias(m.Identity) || expected[key] != p.Kind || seen[key] {
			return driver.Wrap(driver.ErrorInvalidState, "network", "invalid persisted network link proof", nil)
		}
		if p.Namespace == m.Spec.Name && (p.Inode != m.NamespaceInode || p.Device != m.NamespaceDevice) {
			return driver.Wrap(driver.ErrorInvalidState, "network", "network link namespace proof mismatch", nil)
		}
		seen[key] = true
	}
	return nil
}
func checkNamespaceFile(ns *os.File, device, inode uint64) error {
	info, err := ns.Stat()
	if err != nil {
		return err
	}
	stat := info.Sys().(*syscall.Stat_t)
	if inode == 0 || stat.Ino != inode || uint64(stat.Dev) != device {
		return driver.Wrap(driver.ErrorInvalidState, "network", "network namespace incarnation changed", nil)
	}
	return nil
}

func (d Driver) deleteRecordedLinks(ctx context.Context, m networkOwnership) error {
	// Delete veth endpoints before their bridges; link deletion removes routes
	// attached to that link without issuing name-based route/address commands.
	for _, kind := range []string{"veth", "bridge"} {
		for _, proof := range m.Links {
			if proof.Kind != kind {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			ns, h, err := openAttachmentNamespace(proof.Namespace)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			err = func() error {
				defer ns.Close()
				defer h.Close()
				if err := checkNamespaceFile(ns, proof.Device, proof.Inode); err != nil {
					return err
				}
				link, err := h.LinkByName(proof.Name)
				if missingLink(err) {
					return nil
				}
				if err != nil {
					return err
				}
				if proof.Version != 1 || proof.Alias != networkAlias(m.Identity) || link.Attrs().Index != proof.Index || link.Type() != proof.Kind || link.Attrs().Alias != proof.Alias {
					return driver.Wrap(driver.ErrorInvalidState, "network", "network link incarnation changed", nil)
				}
				return deleteInspectedLink(h, link)
			}()
			if err != nil {
				return err
			}
		}
	}
	_, root := d.roots()
	ns, err := os.Open(filepath.Join(root, m.Spec.Name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer ns.Close()
	if err := checkNamespaceFile(ns, m.NamespaceDevice, m.NamespaceInode); err != nil {
		return err
	}
	return anchorCommand("--anchor-delete", m.Spec.Name, fmt.Sprint(m.NamespaceDevice), fmt.Sprint(m.NamespaceInode))
}
