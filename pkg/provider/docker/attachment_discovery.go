package docker

import (
	"context"
	"encoding/json"
	"fmt"
	dockernet "github.com/docker/docker/api/types/network"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"reflect"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Daemon-managed endpoints can be inventoried without inventing host veth proof.
// Raw veth configurations require their own kernel inventory and remain unknown.
func (s *Substrate) DiscoverManagedAttachments(ctx context.Context, h substrate.NodeHandle, requests []driver.AttachmentRequest) (map[string]driver.AttachmentResult, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || id.Validate() != nil || s == nil || s.cli == nil || h.ID == "" {
		return nil, fmt.Errorf("managed discovery identity required")
	}
	if err := s.validateAttachmentHandle(ctx, &h); err != nil {
		return nil, err
	}
	before, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return nil, err
	}
	if before.NetworkSettings == nil {
		return nil, fmt.Errorf("network inventory unavailable")
	}
	// The none pseudo-network has no independently managed attachment.
	endpoints := map[string]string{}
	for name, ep := range before.NetworkSettings.Networks {
		if ep == nil {
			return nil, fmt.Errorf("endpoint unavailable")
		}
		if name == "none" && ep.EndpointID == "" {
			if before.HostConfig == nil || before.HostConfig.NetworkMode != "none" {
				return nil, fmt.Errorf("unproven null network")
			}
			nw, err := s.cli.NetworkInspect(ctx, ep.NetworkID, dockernet.InspectOptions{})
			if err != nil {
				return nil, err
			}
			if nw.ID != ep.NetworkID || nw.Driver != "null" {
				return nil, fmt.Errorf("unproven null network")
			}
			continue
		}
		if ep.NetworkID == "" {
			return nil, fmt.Errorf("endpoint identity unavailable")
		}
		if _, exists := endpoints[ep.NetworkID]; exists {
			return nil, fmt.Errorf("duplicate endpoint")
		}
		endpoints[ep.NetworkID] = name
	}
	if len(endpoints) != len(requests) {
		return nil, fmt.Errorf("endpoint inventory differs from admitted links")
	}
	result := map[string]driver.AttachmentResult{}
	used := map[string]bool{}
	for _, req := range requests {
		var target networkState
		if json.Unmarshal(req.NetworkState, &target) != nil || !target.NAT || target.NetworkID == "" {
			return nil, fmt.Errorf("raw host link discovery requires kernel inventory")
		}
		if req.Name == "" || used[target.NetworkID] {
			return nil, fmt.Errorf("ambiguous attachment")
		}
		if _, exists := result[req.Name]; exists {
			return nil, fmt.Errorf("duplicate attachment name")
		}
		_, exists := endpoints[target.NetworkID]
		if !exists {
			return nil, fmt.Errorf("attachment missing")
		}
		raw, _ := json.Marshal(attachmentState{Kind: substrate.NICKindDockerNAT, NetworkID: target.NetworkID})
		observed, err := s.Observe(ctx, h, req, raw)
		if err != nil {
			return nil, err
		}
		result[req.Name] = observed
		used[target.NetworkID] = true
	}
	// The daemon list does not include manually injected veths. Pin the
	// actual sandbox and account independently for every non-loopback link.
	if before.NetworkSettings.SandboxKey == "" || before.State == nil || before.State.Pid <= 0 {
		return nil, fmt.Errorf("kernel network inventory unavailable")
	}
	ns, err := netns.GetFromPath(before.NetworkSettings.SandboxKey)
	if err != nil {
		return nil, err
	}
	defer ns.Close()
	processNS := fmt.Sprintf("/proc/%d/ns/net", before.State.Pid)
	if err = validateDiscoveryNamespaceBinding(before.NetworkSettings.SandboxKey, processNS, int(ns)); err != nil {
		return nil, err
	}
	links, err := netlink.NewHandleAt(ns)
	if err != nil {
		return nil, err
	}
	defer links.Close()
	actual, err := links.LinkList()
	if err != nil {
		return nil, err
	}
	macs := map[string]bool{}
	for _, name := range endpoints {
		mac := before.NetworkSettings.Networks[name].MacAddress
		if mac == "" || macs[mac] {
			return nil, fmt.Errorf("ambiguous endpoint MAC inventory")
		}
		macs[mac] = true
	}
	if err = validateDiscoveredKernelLinks(actual, macs); err != nil {
		return nil, err
	}
	after, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before.NetworkSettings, after.NetworkSettings) || !reflect.DeepEqual(before.State, after.State) {
		return nil, fmt.Errorf("attachment inventory changed during observation")
	}
	if err = validateDiscoveryNamespaceBinding(before.NetworkSettings.SandboxKey, processNS, int(ns)); err != nil {
		return nil, err
	}
	return result, nil
}

func validateDiscoveredKernelLinks(actual []netlink.Link, macs map[string]bool) error {
	for _, link := range actual {
		if link.Type() == "device" && link.Attrs().Name == "lo" && link.Attrs().Index == 1 {
			continue
		}
		if link.Type() != "veth" || !macs[link.Attrs().HardwareAddr.String()] {
			return fmt.Errorf("unaccounted kernel network attachment")
		}
		delete(macs, link.Attrs().HardwareAddr.String())
	}
	if len(macs) != 0 {
		return fmt.Errorf("endpoint missing from kernel inventory")
	}
	return nil
}

func validateDiscoveryNamespaceBinding(sandbox, process string, pinned int) error {
	var original unix.Stat_t
	if err := unix.Fstat(pinned, &original); err != nil {
		return err
	}
	for _, path := range []string{sandbox, process} {
		var current unix.Stat_t
		if err := unix.Stat(path, &current); err != nil {
			return err
		}
		if current.Dev != original.Dev || current.Ino != original.Ino {
			return fmt.Errorf("container namespace binding changed")
		}
	}
	return nil
}
