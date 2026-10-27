package network

import (
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/vishvananda/netlink"
)

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
