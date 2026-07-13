package network

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// CreateRootBridgeProxy connects a root-netns bridge to an isolated network's
// bridge. System libvirt can only attach domains to links visible in its own
// network namespace.
func CreateRootBridgeProxy(spec driver.IsolatedNetworkSpec) error {
	if spec.RootBridge == "" {
		return nil
	}
	rootBridge, err := ensureRootBridge(spec.RootBridge)
	if err != nil {
		return err
	}
	rootLink, err := netlink.LinkByName(spec.RootEnd)
	if err != nil {
		attrs := netlink.NewLinkAttrs()
		attrs.Name = spec.RootEnd
		if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: attrs, PeerName: spec.NamespaceEnd}); err != nil {
			return fmt.Errorf("add libvirt transit veth: %w", err)
		}
		rootLink, err = netlink.LinkByName(spec.RootEnd)
		if err != nil {
			return err
		}
		peer, err := netlink.LinkByName(spec.NamespaceEnd)
		if err != nil {
			return err
		}
		target, err := netns.GetFromName(spec.Name)
		if err != nil {
			return err
		}
		defer target.Close()
		if err := netlink.LinkSetNsFd(peer, int(target)); err != nil {
			return fmt.Errorf("move libvirt transit veth to %s: %w", spec.Name, err)
		}
	}
	if err := netlink.LinkSetMaster(rootLink, rootBridge); err != nil {
		return fmt.Errorf("attach %s to %s: %w", spec.RootEnd, spec.RootBridge, err)
	}
	if err := netlink.LinkSetUp(rootLink); err != nil {
		return err
	}
	return inNetns(spec.Name, func() error {
		peer, err := netlink.LinkByName(spec.NamespaceEnd)
		if err != nil {
			return err
		}
		bridge, err := netlink.LinkByName(spec.Bridge)
		if err != nil {
			return err
		}
		if err := netlink.LinkSetMaster(peer, bridge); err != nil {
			return fmt.Errorf("attach %s to %s: %w", spec.NamespaceEnd, spec.Bridge, err)
		}
		return netlink.LinkSetUp(peer)
	})
}

func ensureRootBridge(name string) (*netlink.Bridge, error) {
	if existing, err := netlink.LinkByName(name); err == nil {
		bridge, ok := existing.(*netlink.Bridge)
		if !ok {
			return nil, fmt.Errorf("root link %s is not a bridge", name)
		}
		if err := netlink.LinkSetUp(bridge); err != nil {
			return nil, err
		}
		return bridge, nil
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	bridge := &netlink.Bridge{LinkAttrs: attrs}
	if err := netlink.LinkAdd(bridge); err != nil {
		return nil, fmt.Errorf("add root bridge %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(bridge); err != nil {
		return nil, err
	}
	return bridge, nil
}

func DeleteRootBridgeProxy(spec driver.IsolatedNetworkSpec) error {
	if spec.RootEnd != "" {
		if link, err := netlink.LinkByName(spec.RootEnd); err == nil {
			if err := netlink.LinkDel(link); err != nil {
				return err
			}
		}
	}
	if spec.RootBridge != "" {
		if bridge, err := netlink.LinkByName(spec.RootBridge); err == nil {
			return netlink.LinkDel(bridge)
		}
	}
	return nil
}

func RootBridgeProxyExists(spec driver.IsolatedNetworkSpec) bool {
	if spec.RootBridge == "" {
		return true
	}
	_, bridgeErr := netlink.LinkByName(spec.RootBridge)
	_, linkErr := netlink.LinkByName(spec.RootEnd)
	return bridgeErr == nil && linkErr == nil && LinkExists(spec.Name, spec.NamespaceEnd)
}
