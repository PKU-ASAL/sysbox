package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/docker/docker/errdefs"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// ... (AttachNIC unchanged)

// AttachNIC creates a network attachment for the container. Two paths:
//   - KindHint == NICKindDockerNAT (or DockerNetID != ""): uses
//     docker network connect — the Docker daemon manages the veth/bridge.
//   - Otherwise: creates a veth pair, wires into isolated netns bridge,
//     moves guest-end into container netns (the "cold-plug" path).
type attachmentState struct {
	Link        *network.AttachmentLink `json:"link,omitempty"`
	Kind        string                  `json:"kind"`
	HostEnd     string                  `json:"host_end"`
	GuestEnd    string                  `json:"guest_end"`
	NetNS       string                  `json:"netns"`
	NetworkID   string                  `json:"network_id"`
	GuestDevice string                  `json:"guest_device"`
}
type networkState struct {
	NetNS     string `json:"netns"`
	Bridge    string `json:"bridge"`
	NetworkID string `json:"docker_network_id"`
	NAT       bool   `json:"nat"`
}
type linkRequest struct {
	Target                                                            network.BridgeTarget
	Name, NetNS, IP, Gateway, MAC, GuestDevice, KindHint, DockerNetID string
	Aliases                                                           []string
}
type attachedNIC struct {
	Kind, HostEnd, GuestEnd, IP, NetNS string
	Link                               network.AttachmentLink
}

func (s *Substrate) Attach(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest) (driver.AttachmentResult, error) {
	var target networkState
	if err := json.Unmarshal(req.NetworkState, &target); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "docker", "decode network state", err)
	}
	if !target.NAT && len(req.Aliases) > 0 {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorUnsupported, "docker", "network aliases require a Docker-managed network", nil)
	}
	if err := s.validateAttachmentHandle(ctx, &h); err != nil {
		return driver.AttachmentResult{}, err
	}
	if err := req.NetworkIdentity.Validate(); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "docker", "target network identity required; rebuild old state", err)
	}
	if target.NAT {
		if err := s.validateAttachmentNetwork(ctx, req.NetworkIdentity, target.NetworkID); err != nil {
			return driver.AttachmentResult{}, err
		}
	} else if target.NetworkID != "" {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "docker", "conflicting network target kind", nil)
	}
	ip := firstPrefix(req.IPPrefixes)
	guest := dockerGuestName(req.Name)
	if target.NAT {
		guest = ""
	}
	kind := ""
	if target.NAT {
		kind = substrate.NICKindDockerNAT
	}
	attached, err := s.attachNIC(ctx, h, linkRequest{Target: network.BridgeTarget{Identity: req.NetworkIdentity, NetworkNamespace: target.NetNS, Bridge: target.Bridge}, Name: req.Name, NetNS: target.NetNS, IP: ip, Gateway: req.Gateway, MAC: req.MAC, GuestDevice: guest, KindHint: kind, DockerNetID: target.NetworkID, Aliases: append([]string(nil), req.Aliases...)})
	if err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorUnavailable, "docker", "attach network", err)
	}
	if target.NAT {
		if hs, ok := h.Provider.(*HandleState); ok && hs.RemoveDefaultBridge {
			if err := s.cli.NetworkDisconnect(ctx, "bridge", h.ID, true); err != nil {
				_ = s.cli.NetworkDisconnect(ctx, target.NetworkID, h.ID, true)
				return driver.AttachmentResult{}, driver.Wrap(driver.ErrorUnavailable, "docker", "disconnect default bridge", err)
			}
			hs.RemoveDefaultBridge = false
		}
	}
	state := attachmentState{Kind: attached.Kind, HostEnd: attached.HostEnd, GuestEnd: attached.GuestEnd, NetNS: attached.NetNS, NetworkID: target.NetworkID, GuestDevice: guest}
	if !target.NAT {
		state.Link = &attached.Link
	}
	raw, _ := json.Marshal(state)
	return driver.AttachmentResult{Driver: "docker", GuestDevice: guest, State: raw}, nil
}

func (s *Substrate) Observe(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest, raw json.RawMessage) (driver.AttachmentResult, error) {
	if err := s.validateAttachmentHandle(ctx, &h); err != nil {
		return driver.AttachmentResult{}, err
	}
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "docker", "decode attachment state", err)
	}
	if st.NetworkID != "" {
		if err := s.validateNATAttachment(ctx, req, st.NetworkID); err != nil {
			return driver.AttachmentResult{}, err
		}
		ins, err := s.cli.ContainerInspect(ctx, h.ID)
		if err != nil {
			category := driver.ErrorUnavailable
			if errdefs.IsNotFound(err) {
				category = driver.ErrorNotFound
			}
			return driver.AttachmentResult{}, driver.Wrap(category, "docker", "inspect attachment", err)
		}
		found := false
		for _, endpoint := range ins.NetworkSettings.Networks {
			if endpoint.NetworkID == st.NetworkID {
				if !containsAliases(endpoint.Aliases, req.Aliases) {
					return driver.AttachmentResult{}, driver.Wrap(driver.ErrorNotFound, "docker", "attachment network aliases drifted", nil)
				}
				found = true
				break
			}
		}
		if !found {
			return driver.AttachmentResult{}, driver.Wrap(driver.ErrorNotFound, "docker", "attachment not found", nil)
		}
	} else {
		if st.Link == nil {
			return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "docker", "attachment ownership proof required", nil)
		}
		id, _ := substrate.OperationIdentityFromContext(ctx)
		alias, err := network.AttachmentAlias(id, req.Name)
		if err != nil {
			return driver.AttachmentResult{}, err
		}
		var target networkState
		if err := json.Unmarshal(req.NetworkState, &target); err != nil {
			return driver.AttachmentResult{}, err
		}
		bridge, err := network.OpenOwnedBridge(ctx, network.BridgeTarget{Identity: req.NetworkIdentity, NetworkNamespace: target.NetNS, Bridge: target.Bridge})
		if err != nil {
			return driver.AttachmentResult{}, err
		}
		defer bridge.Close()
		if err := bridge.ObserveAttachment(ctx, *st.Link, alias); err != nil {
			return driver.AttachmentResult{}, err
		}
	}
	return driver.AttachmentResult{Driver: "docker", GuestDevice: st.GuestDevice, State: raw}, nil
}

func (s *Substrate) Delete(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest, raw json.RawMessage) error {
	if err := s.validateAttachmentHandle(ctx, &h); err != nil {
		return err
	}
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "decode attachment state", err)
	}
	if st.NetworkID != "" {
		if err := s.validateNATAttachment(ctx, req, st.NetworkID); err != nil {
			if driver.IsCategory(err, driver.ErrorNotFound) {
				return nil
			}
			return err
		}
		err := s.cli.NetworkDisconnect(ctx, st.NetworkID, h.ID, true)
		if errdefs.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return driver.Wrap(driver.ErrorUnavailable, "docker", "disconnect attachment", err)
		}
		return nil
	}
	if st.Link == nil {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "attachment ownership proof required", nil)
	}
	id, _ := substrate.OperationIdentityFromContext(ctx)
	alias, err := network.AttachmentAlias(id, req.Name)
	if err != nil {
		return err
	}
	if err := network.DeleteOwnedAttachment(ctx, *st.Link, alias); err != nil {
		return driver.Wrap(driver.ErrorUnavailable, "docker", "delete attachment link", err)
	}
	return nil
}

func (s *Substrate) validateNATAttachment(ctx context.Context, req driver.AttachmentRequest, networkID string) error {
	var target networkState
	if err := json.Unmarshal(req.NetworkState, &target); err != nil {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "target network state required; rebuild attachment", err)
	}
	if !target.NAT || target.NetworkID != networkID {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "attachment target network ID conflict", nil)
	}
	return s.validateAttachmentNetwork(ctx, req.NetworkIdentity, networkID)
}

func (s *Substrate) validateAttachmentHandle(ctx context.Context, h *substrate.NodeHandle) error {
	err := s.validateManagedHandle(ctx, h)
	if errdefs.IsNotFound(err) {
		return driver.Wrap(driver.ErrorNotFound, "docker", "attachment container absent", err)
	}
	return err
}

func (s *Substrate) attachNIC(ctx context.Context, h substrate.NodeHandle, req linkRequest) (attachedNIC, error) {
	// Docker NAT bridge: delegate to docker network connect.
	if req.DockerNetID != "" || req.KindHint == substrate.NICKindDockerNAT {
		if err := s.ConnectContainerToNetwork(ctx, h.ID, req.DockerNetID, req.IP, req.MAC, req.Aliases); err != nil {
			return attachedNIC{}, fmt.Errorf("docker network connect: %w", err)
		}
		return attachedNIC{
			Kind: substrate.NICKindDockerNAT,
			IP:   req.IP,
		}, nil
	}

	// Isolated network: veth pair + netns injection.
	id, _ := substrate.OperationIdentityFromContext(ctx)
	alias, err := network.AttachmentAlias(id, req.Name)
	if err != nil {
		return attachedNIC{}, err
	}
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return attachedNIC{}, fmt.Errorf("inspect container: %w", err)
	}
	containerPID := ins.State.Pid
	if containerPID == 0 {
		return attachedNIC{}, fmt.Errorf("container %s is not running", h.ID)
	}

	hs, _ := h.Provider.(*HandleState)
	containerName := "sysbox-unknown"
	if hs != nil && hs.ContainerName != "" {
		containerName = hs.ContainerName
	}

	// Derive deterministic veth names from the container name so leftover
	// cleanup on retry works reliably.
	hostEnd := vethName("vh", containerName+"-"+req.Name)
	guestEnd := vethName("vg", containerName+"-"+req.Name)

	// Create the veth pair inside the network's netns and attach the
	// host-end to the bridge. Idempotent: reuses existing devices.
	bridge, err := network.OpenOwnedBridge(ctx, req.Target)
	if err != nil {
		return attachedNIC{}, err
	}
	defer bridge.Close()
	pair, err := bridge.CreateVethPair(ctx, network.VethSpec{
		OwnerAlias: alias,
		HostEnd:    hostEnd,
		GuestEnd:   guestEnd,
	})
	if err != nil {
		return attachedNIC{}, fmt.Errorf("create veth pair: %w", err)
	}

	// Move the guest-end into the container's netns and configure it.
	if err := attachVethToContainer(ctx, bridge, pair, req.GuestDevice, containerPID, req.IP, req.Gateway, req.MAC); err != nil {
		return attachedNIC{}, err
	}
	proof, err := bridge.CaptureAttachment(ctx, pair.HostEnd, alias)
	if err != nil {
		return attachedNIC{}, err
	}

	return attachedNIC{
		Link:     proof,
		Kind:     substrate.NICKindVeth,
		HostEnd:  pair.HostEnd,
		GuestEnd: pair.GuestEnd,
		IP:       req.IP,
		NetNS:    req.NetNS,
	}, nil
}

func (s *Substrate) validateAttachmentNetwork(ctx context.Context, id substrate.OperationIdentity, networkID string) error {
	if err := id.Validate(); err != nil {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "target network identity required; rebuild old state", err)
	}
	if networkID == "" {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "target network ID required", nil)
	}
	info, err := s.ReadManagedNetwork(substrate.WithOperationIdentity(ctx, id), substrate.ManagedNetworkSpec{Name: networkID})
	if err != nil {
		return err
	}
	if info.ID != networkID {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "target network immutable ID mismatch", nil)
	}
	return nil
}

func containsAliases(observed, desired []string) bool {
	available := make(map[string]struct{}, len(observed))
	for _, alias := range observed {
		available[alias] = struct{}{}
	}
	for _, alias := range desired {
		if _, exists := available[alias]; !exists {
			return false
		}
	}
	return true
}

func firstPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
func dockerGuestName(name string) string {
	if len(name) <= 15 {
		return name
	}
	return fmt.Sprintf("net-%08x", fnv32([]byte(name)))
}

// vethName generates a deterministic, collision-resistant veth interface name
// from a prefix and container name. Names must be ≤15 chars (IFNAMSIZ-1).
// Uses FNV-1a hash to avoid truncation-based collisions: two containers whose
// names share a 12-char prefix (e.g. sysbox-frontend-web-1 and
// sysbox-frontend-web-2) must produce different interface names.
func vethName(prefix, containerName string) string {
	name := containerName
	if len(name) > 7 && name[:7] == "sysbox-" {
		name = name[7:]
	}
	// If the name fits within the 15-char limit (prefix + "-" + name),
	// use it directly; otherwise hash the name for a short stable suffix.
	maxNameLen := 15 - len(prefix) - 1 // prefix + "-" + suffix
	if len(name) <= maxNameLen {
		return prefix + "-" + name
	}
	// FNV-1a 32-bit hash, formatted as 8-hex chars.
	h := fnv32([]byte(name))
	return fmt.Sprintf("%s-%08x", prefix, h)
}

// fnv32 computes FNV-1a 32-bit hash (simple, no import needed).
func fnv32(data []byte) uint32 {
	const (
		offsetBasis = uint32(2166136261)
		prime       = uint32(16777619)
	)
	h := offsetBasis
	for _, b := range data {
		h ^= uint32(b)
		h *= prime
	}
	return h
}

// attachVethToContainer pins the destination before moving the proven peer.
// Errors retain the owned pair for inspection; they never delete a link by name.
func attachVethToContainer(ctx context.Context, bridge *network.OwnedBridge, pair network.VethHandle, targetName string, containerPID int, ip, gateway, mac string) error {
	containerNS, err := netns.GetFromPid(containerPID)
	if err != nil {
		return fmt.Errorf("open container namespace: %w", err)
	}
	defer containerNS.Close()
	h, err := netlink.NewHandleAt(containerNS)
	if err != nil {
		return err
	}
	defer h.Close()
	addr, err := netlink.ParseAddr(ip)
	if err != nil {
		return fmt.Errorf("parse attachment IP: %w", err)
	}
	var hardwareAddress net.HardwareAddr
	if mac != "" {
		hardwareAddress, err = net.ParseMAC(mac)
		if err != nil {
			return err
		}
	}
	if gateway != "" && net.ParseIP(gateway) == nil {
		return fmt.Errorf("invalid gateway")
	}
	if err := bridge.MoveVethPeer(ctx, pair, int(containerNS)); err != nil {
		return err
	}
	link, err := h.LinkByName(pair.GuestEnd)
	if err != nil {
		return err
	}
	if link.Type() != "veth" || link.Attrs().Alias != pair.OwnerAlias || link.Attrs().ParentIndex != pair.HostIndex {
		return fmt.Errorf("moved veth identity conflict")
	}
	if targetName == "" {
		targetName = "eth0"
	}
	if err := h.LinkSetDown(link); err != nil {
		return err
	}
	if err := h.LinkSetName(link, targetName); err != nil {
		return err
	}
	if hardwareAddress != nil {
		if err := h.LinkSetHardwareAddr(link, hardwareAddress); err != nil {
			return err
		}
	}
	if err := h.AddrReplace(link, addr); err != nil {
		return err
	}
	if err := h.LinkSetUp(link); err != nil {
		return err
	}
	if lo, err := h.LinkByName("lo"); err == nil {
		if err := h.LinkSetUp(lo); err != nil {
			return err
		}
	}
	if gateway != "" {
		_, defaultNet, _ := net.ParseCIDR("0.0.0.0/0")
		if err := h.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: defaultNet, Gw: net.ParseIP(gateway)}); err != nil {
			return err
		}
	}
	return nil
}
