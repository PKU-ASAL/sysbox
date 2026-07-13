package network

import (
	"context"

	"github.com/oslab/sysbox/pkg/driver"
)

type Driver struct{}

func (Driver) CreateIsolated(_ context.Context, spec driver.IsolatedNetworkSpec) error {
	if err := CreateNetns(spec.Name); err != nil {
		return err
	}
	if err := CreateBridge(BridgeConfig{NetnsName: spec.Name, BridgeName: spec.Bridge, CIDR: spec.CIDR}); err != nil {
		return err
	}
	if err := CreateRootBridgeProxy(spec); err != nil {
		_ = DeleteBridge(BridgeConfig{NetnsName: spec.Name, BridgeName: spec.Bridge})
		_ = DeleteNetns(spec.Name)
		_ = DeleteRootBridgeProxy(spec)
		return err
	}
	return nil
}
func (Driver) DeleteIsolated(_ context.Context, spec driver.IsolatedNetworkSpec) error {
	if err := DeleteRootBridgeProxy(spec); err != nil {
		return err
	}
	if err := DeleteBridge(BridgeConfig{NetnsName: spec.Name, BridgeName: spec.Bridge}); err != nil {
		return err
	}
	return DeleteNetns(spec.Name)
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
func (Driver) DeleteAttachment(_ context.Context, kind, hostEnd, namespace string) error {
	if kind == "tap" {
		return DeleteTapDevice(hostEnd, namespace)
	}
	return DeleteVethPair(VethHandle{HostEnd: hostEnd, NetnsName: namespace})
}
