package network

import (
	"context"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/vishvananda/netlink"
)

type Driver struct {
	ownershipRoot, namespaceRoot string
	linkLookup                   func(string, string) (netlink.Link, error)
	addressLookup                func(string, netlink.Link) ([]netlink.Addr, error)
	routeLookup                  func(netlink.Link) ([]netlink.Route, error)
}

func (d Driver) CreateIsolated(ctx context.Context, spec driver.IsolatedNetworkSpec) error {
	return d.createOwnedIsolated(ctx, spec)
}
func (d Driver) DeleteIsolated(ctx context.Context, spec driver.IsolatedNetworkSpec) error {
	return d.deleteOwnedIsolated(ctx, spec)
}
func (Driver) NetworkHealthy(_ context.Context, spec driver.IsolatedNetworkSpec) (bool, string) {
	if !NetnsExists(spec.Name) {
		return false, "network namespace missing"
	}
	if spec.Bridge != "" && !BridgeExists(spec.Name, spec.Bridge) {
		return false, "bridge missing"
	}
	if spec.RootBridge != "" && !RootBridgeProxyExists(spec) {
		return false, "libvirt root bridge proxy missing"
	}
	return true, ""
}
func (Driver) LinkHealthy(_ context.Context, namespace, name string) bool {
	return LinkExists(namespace, name)
}
