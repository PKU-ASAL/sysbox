package network

import (
	"errors"
	"fmt"
	"net"
	"syscall"

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
	if err := configureRootIngress(rootBridge, spec); err != nil {
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
	if err := deleteHostRoute(spec.RootBridge, spec.CIDR); err != nil {
		return err
	}
	if hasRootIngress(spec) {
		for _, route := range spec.RootRoutes {
			if err := deleteHostRouteVia(spec.RootBridge, route.Destination, route.Via); err != nil {
				return err
			}
		}
		if err := deleteRootAddress(spec.RootBridge, spec.RootAddress); err != nil {
			return err
		}
	}
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
	if bridgeErr != nil || linkErr != nil || !LinkExists(spec.Name, spec.NamespaceEnd) {
		return false
	}
	if !hasRootIngress(spec) {
		return true
	}
	if !hostRouteExists(spec.RootBridge, spec.CIDR) || !rootAddressExists(spec.RootBridge, spec.RootAddress) {
		return false
	}
	for _, route := range spec.RootRoutes {
		if !hostRouteViaExists(spec.RootBridge, route.Destination, route.Via) {
			return false
		}
	}
	return true
}

func hasRootIngress(spec driver.IsolatedNetworkSpec) bool {
	return spec.RootAddress != "" || len(spec.RootRoutes) > 0
}

func configureRootIngress(bridge *netlink.Bridge, spec driver.IsolatedNetworkSpec) error {
	// 没有声明 root ingress 的网络不应向宿主机暴露直连路由。删除旧版本
	// 遗留的同名路由，避免它绕过拓扑内的路由器和防火墙。
	if !hasRootIngress(spec) {
		return deleteHostRoute(spec.RootBridge, spec.CIDR)
	}
	if spec.RootAddress == "" {
		return fmt.Errorf("root ingress routes require root_address")
	}
	if err := ensureRootAddress(bridge, spec.RootAddress); err != nil {
		return err
	}
	if err := ensureHostRoute(bridge, spec.CIDR); err != nil {
		return err
	}
	for _, route := range spec.RootRoutes {
		if err := ensureHostRouteVia(bridge, route.Destination, route.Via); err != nil {
			return err
		}
	}
	return nil
}

func ensureRootAddress(bridge *netlink.Bridge, cidr string) error {
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("parse root address %s: %w", cidr, err)
	}
	if addr.IP.To4() == nil {
		return fmt.Errorf("root address %s is not IPv4", cidr)
	}
	if err := netlink.AddrReplace(bridge, addr); err != nil {
		return fmt.Errorf("add root address %s via %s: %w", cidr, bridge.Attrs().Name, err)
	}
	return nil
}

func deleteRootAddress(bridgeName, cidr string) error {
	if bridgeName == "" || cidr == "" {
		return nil
	}
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return nil
	}
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("parse root address %s: %w", cidr, err)
	}
	if err := netlink.AddrDel(bridge, addr); err != nil && !isAddressNotFound(err) {
		return fmt.Errorf("delete root address %s via %s: %w", cidr, bridgeName, err)
	}
	return nil
}

func rootAddressExists(bridgeName, cidr string) bool {
	if bridgeName == "" || cidr == "" {
		return false
	}
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return false
	}
	want, err := netlink.ParseAddr(cidr)
	if err != nil {
		return false
	}
	wantOnes, wantBits := want.Mask.Size()
	addresses, err := netlink.AddrList(bridge, netlink.FAMILY_V4)
	if err != nil {
		return false
	}
	for _, address := range addresses {
		ones, bits := address.Mask.Size()
		if bits == wantBits && ones == wantOnes && address.IP.Equal(want.IP) {
			return true
		}
	}
	return false
}

func ensureHostRoute(bridge *netlink.Bridge, cidr string) error {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parse isolated network CIDR %s: %w", cidr, err)
	}
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: bridge.Attrs().Index, Dst: network, Scope: netlink.SCOPE_LINK}); err != nil {
		return fmt.Errorf("add host route %s via %s: %w", network, bridge.Attrs().Name, err)
	}
	return nil
}

func ensureHostRouteVia(bridge *netlink.Bridge, cidr, via string) error {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parse host route CIDR %s: %w", cidr, err)
	}
	gateway := net.ParseIP(via)
	if gateway == nil || gateway.To4() == nil {
		return fmt.Errorf("host route gateway %s is not IPv4", via)
	}
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: bridge.Attrs().Index, Dst: network, Gw: gateway.To4()}); err != nil {
		return fmt.Errorf("add host route %s via %s dev %s: %w", network, via, bridge.Attrs().Name, err)
	}
	return nil
}

func deleteHostRoute(bridgeName, cidr string) error {
	if bridgeName == "" || cidr == "" {
		return nil
	}
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return nil
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parse isolated network CIDR %s: %w", cidr, err)
	}
	if err := netlink.RouteDel(&netlink.Route{LinkIndex: bridge.Attrs().Index, Dst: network}); err != nil && !isRouteNotFound(err) {
		return fmt.Errorf("delete host route %s via %s: %w", network, bridgeName, err)
	}
	return nil
}

func deleteHostRouteVia(bridgeName, cidr, via string) error {
	if bridgeName == "" || cidr == "" || via == "" {
		return nil
	}
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return nil
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parse host route CIDR %s: %w", cidr, err)
	}
	gateway := net.ParseIP(via)
	if gateway == nil || gateway.To4() == nil {
		return fmt.Errorf("host route gateway %s is not IPv4", via)
	}
	if err := netlink.RouteDel(&netlink.Route{LinkIndex: bridge.Attrs().Index, Dst: network, Gw: gateway.To4()}); err != nil && !isRouteNotFound(err) {
		return fmt.Errorf("delete host route %s via %s dev %s: %w", network, via, bridgeName, err)
	}
	return nil
}

func hostRouteExists(bridgeName, cidr string) bool {
	return hostRouteViaExists(bridgeName, cidr, "")
}

func hostRouteViaExists(bridgeName, cidr, via string) bool {
	if bridgeName == "" || cidr == "" {
		return false
	}
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return false
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	routes, err := netlink.RouteList(bridge, netlink.FAMILY_V4)
	if err != nil {
		return false
	}
	for _, route := range routes {
		if route.Dst == nil || route.Dst.String() != network.String() {
			continue
		}
		if via == "" {
			if route.Gw == nil {
				return true
			}
			continue
		}
		gateway := net.ParseIP(via)
		if gateway != nil && route.Gw != nil && route.Gw.Equal(gateway) {
			return true
		}
	}
	return false
}

func isRouteNotFound(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT)
}

func isAddressNotFound(err error) bool {
	return errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT)
}
