package docker

import (
	"context"
	"encoding/json"
	"fmt"
	dockernet "github.com/docker/docker/api/types/network"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"reflect"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Discover exact daemon endpoints and owned raw veth peers without mutation.
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
		if name == "none" {
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
	if len(endpoints) > len(requests) {
		return nil, fmt.Errorf("endpoint inventory differs from admitted links")
	}
	result := map[string]driver.AttachmentResult{}
	used := map[string]bool{}
	rawRequests := []driver.AttachmentRequest{}
	for _, req := range requests {
		var target networkState
		if json.Unmarshal(req.NetworkState, &target) != nil {
			return nil, fmt.Errorf("invalid network target")
		}
		if !target.NAT && target.NetworkID == "" {
			rawRequests = append(rawRequests, req)
			continue
		}
		if !target.NAT || target.NetworkID == "" {
			return nil, fmt.Errorf("conflicting network target")
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
	if len(used) != len(endpoints) {
		return nil, fmt.Errorf("unaccounted daemon endpoint")
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
	rawPeers := map[int]netlink.Link{}
	for _, req := range rawRequests {
		if req.Name == "" {
			return nil, fmt.Errorf("attachment name required")
		}
		if _, exists := result[req.Name]; exists {
			return nil, fmt.Errorf("duplicate attachment name")
		}
		observed, peer, err := s.discoverRawAttachment(ctx, h, req, links, int(ns))
		if err != nil {
			return nil, err
		}
		if _, exists := rawPeers[peer.Attrs().Index]; exists {
			return nil, fmt.Errorf("duplicate raw peer")
		}
		result[req.Name] = observed
		rawPeers[peer.Attrs().Index] = peer
	}
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
	remaining := make([]netlink.Link, 0, len(actual))
	for _, link := range actual {
		if peer, ok := rawPeers[link.Attrs().Index]; ok {
			if !reflect.DeepEqual(peer.Attrs(), link.Attrs()) || peer.Type() != link.Type() {
				return nil, fmt.Errorf("raw peer changed during inventory")
			}
			delete(rawPeers, link.Attrs().Index)
		} else {
			remaining = append(remaining, link)
		}
	}
	if len(rawPeers) != 0 {
		return nil, fmt.Errorf("raw peer disappeared")
	}
	if err = validateDiscoveredKernelLinks(remaining, macs); err != nil {
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
	for _, req := range rawRequests {
		again, _, err := s.discoverRawAttachment(ctx, h, req, links, int(ns))
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(result[req.Name], again) {
			return nil, fmt.Errorf("raw attachment changed during inventory")
		}
	}
	for _, req := range rawRequests {
		var target networkState
		_ = json.Unmarshal(req.NetworkState, &target)
		expected := map[string]ownednetwork.AttachmentLink{}
		for _, other := range rawRequests {
			var st attachmentState
			_ = json.Unmarshal(result[other.Name].State, &st)
			if st.Link != nil && st.Link.Namespace == target.NetNS {
				expected[st.Link.Name] = *st.Link
			}
		}
		bridge, err := ownednetwork.OpenOwnedBridge(ctx, ownednetwork.BridgeTarget{Identity: req.NetworkIdentity, NetworkNamespace: target.NetNS, Bridge: target.Bridge})
		if err != nil {
			return nil, err
		}
		err = bridge.RequireAttachmentInventory(ctx, expected)
		bridge.Close()
		if err != nil {
			return nil, err
		}
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

func (s *Substrate) discoverRawAttachment(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest, guest *netlink.Handle, guestFD int) (driver.AttachmentResult, netlink.Link, error) {
	var zero driver.AttachmentResult
	var target networkState
	if json.Unmarshal(req.NetworkState, &target) != nil || target.NAT || target.NetworkID != "" || target.NetNS == "" || target.Bridge == "" {
		return zero, nil, fmt.Errorf("exact isolated bridge target required")
	}
	hs, ok := h.Provider.(*HandleState)
	if !ok || hs.ContainerName == "" {
		return zero, nil, fmt.Errorf("container identity unavailable")
	}
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return zero, nil, fmt.Errorf("generation required")
	}
	alias, err := ownednetwork.AttachmentAlias(id, req.Name)
	if err != nil {
		return zero, nil, err
	}
	bridge, err := ownednetwork.OpenOwnedBridge(ctx, ownednetwork.BridgeTarget{Identity: req.NetworkIdentity, NetworkNamespace: target.NetNS, Bridge: target.Bridge})
	if err != nil {
		return zero, nil, err
	}
	defer bridge.Close()
	proof, err := bridge.CaptureAttachment(ctx, vethName("vh", hs.ContainerName+"-"+req.Name), alias)
	if err != nil {
		return zero, nil, err
	}
	name := dockerGuestName(req.Name)
	peer, err := guest.LinkByName(name)
	if err != nil {
		return zero, nil, err
	}
	if err = validateDiscoveredRawPeer(peer, proof, name, req.MAC); err != nil {
		return zero, nil, err
	}
	if err = bridge.ObserveVethPeer(ctx, proof, guest, guestFD, peer); err != nil {
		return zero, nil, err
	}
	raw, err := json.Marshal(attachmentState{Kind: substrate.NICKindVeth, HostEnd: proof.Name, GuestEnd: vethName("vg", hs.ContainerName+"-"+req.Name), NetNS: target.NetNS, GuestDevice: name, Link: &proof})
	if err != nil {
		return zero, nil, err
	}
	return driver.AttachmentResult{Driver: "docker", GuestDevice: name, State: raw}, peer, nil
}
func validateDiscoveredRawPeer(peer netlink.Link, proof ownednetwork.AttachmentLink, name, mac string) error {
	if peer == nil || peer.Type() != "veth" || proof.Kind != "veth" || peer.Attrs().Name != name || peer.Attrs().Alias != proof.Alias || peer.Attrs().ParentIndex != proof.Index || mac == "" || peer.Attrs().HardwareAddr.String() != mac {
		return fmt.Errorf("raw veth peer identity conflict")
	}
	return nil
}
