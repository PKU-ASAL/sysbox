package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"runtime"

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
	Kind        string `json:"kind"`
	HostEnd     string `json:"host_end"`
	GuestEnd    string `json:"guest_end"`
	NetNS       string `json:"netns"`
	NetworkID   string `json:"network_id"`
	GuestDevice string `json:"guest_device"`
}
type networkState struct {
	NetNS     string `json:"netns"`
	Bridge    string `json:"bridge"`
	NetworkID string `json:"docker_network_id"`
	NAT       bool   `json:"nat"`
}
type linkRequest struct {
	Name, NetNS, Bridge, IP, Gateway, MAC, GuestDevice, KindHint, DockerNetID string
	Aliases                                                                   []string
}
type attachedNIC struct{ Kind, HostEnd, GuestEnd, IP, NetNS string }

func (s *Substrate) Attach(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest) (driver.AttachmentResult, error) {
	var target networkState
	if err := json.Unmarshal(req.NetworkState, &target); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "docker", "decode network state", err)
	}
	if !target.NAT && len(req.Aliases) > 0 {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorUnsupported, "docker", "network aliases require a Docker-managed network", nil)
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
	attached, err := s.attachNIC(ctx, h, linkRequest{Name: req.Name, NetNS: target.NetNS, Bridge: target.Bridge, IP: ip, Gateway: req.Gateway, MAC: req.MAC, GuestDevice: guest, KindHint: kind, DockerNetID: target.NetworkID, Aliases: append([]string(nil), req.Aliases...)})
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
	raw, _ := json.Marshal(state)
	return driver.AttachmentResult{Driver: "docker", GuestDevice: guest, State: raw}, nil
}

func (s *Substrate) Observe(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest, raw json.RawMessage) (driver.AttachmentResult, error) {
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "docker", "decode attachment state", err)
	}
	if st.NetworkID != "" {
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
	} else if !(network.Driver{}).LinkHealthy(ctx, st.NetNS, st.HostEnd) {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorNotFound, "docker", "attachment link not found", nil)
	}
	return driver.AttachmentResult{Driver: "docker", GuestDevice: st.GuestDevice, State: raw}, nil
}

func (s *Substrate) Delete(ctx context.Context, h substrate.NodeHandle, _ driver.AttachmentRequest, raw json.RawMessage) error {
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "decode attachment state", err)
	}
	if st.NetworkID != "" {
		err := s.cli.NetworkDisconnect(ctx, st.NetworkID, h.ID, true)
		if errdefs.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return driver.Wrap(driver.ErrorUnavailable, "docker", "disconnect attachment", err)
		}
		return nil
	}
	if err := (network.Driver{}).DeleteAttachment(ctx, st.Kind, st.HostEnd, st.NetNS); err != nil {
		return driver.Wrap(driver.ErrorUnavailable, "docker", "delete attachment link", err)
	}
	return nil
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
	pair, err := network.CreateVethPair(network.VethSpec{
		HostEnd:    hostEnd,
		GuestEnd:   guestEnd,
		NetnsName:  req.NetNS,
		BridgeName: req.Bridge,
	})
	if err != nil {
		return attachedNIC{}, fmt.Errorf("create veth pair: %w", err)
	}

	// Move the guest-end into the container's netns and configure it.
	if err := attachVethToContainer(guestEnd, req.GuestDevice, req.NetNS, containerPID, req.IP, req.Gateway, req.MAC); err != nil {
		return attachedNIC{}, err
	}

	return attachedNIC{
		Kind:     substrate.NICKindVeth,
		HostEnd:  pair.HostEnd,
		GuestEnd: pair.GuestEnd,
		IP:       req.IP,
		NetNS:    req.NetNS,
	}, nil
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

// attachVethToContainer moves a guest-end veth from sourceNetns into the
// container's netns (identified by PID), renames it, assigns IP, and
// optionally sets the default gateway.
func attachVethToContainer(guestEnd, targetName, sourceNetnsName string, containerPID int, ip, gateway, mac string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNS, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get root netns: %w", err)
	}
	defer origNS.Close()
	defer func() { _ = netns.Set(origNS) }()

	if sourceNetnsName != "" {
		srcNS, err := netns.GetFromName(sourceNetnsName)
		if err != nil {
			return fmt.Errorf("get source netns %s: %w", sourceNetnsName, err)
		}
		defer srcNS.Close()
		if err := netns.Set(srcNS); err != nil {
			return fmt.Errorf("enter source netns: %w", err)
		}
	}

	link, err := netlink.LinkByName(guestEnd)
	if err != nil {
		return fmt.Errorf("find veth guest end %s: %w", guestEnd, err)
	}

	if err := netlink.LinkSetNsPid(link, containerPID); err != nil {
		return fmt.Errorf("move veth to container netns: %w", err)
	}

	// If anything fails after the veth has been moved into the container
	// netns, clean up the orphaned interface so it doesn't accumulate
	// and conflict with future attempts.
	movedToContainer := true

	if err := netns.Set(origNS); err != nil {
		return fmt.Errorf("return to root netns: %w", err)
	}

	containerNS, err := netns.GetFromPid(containerPID)
	if err != nil {
		return fmt.Errorf("get container netns: %w", err)
	}
	defer containerNS.Close()

	if err := netns.Set(containerNS); err != nil {
		return fmt.Errorf("enter container netns: %w", err)
	}

	// Cleanup: if we moved the veth into the container netns but fail
	// during configuration, remove the orphaned interface so it doesn't
	// conflict with future veth pairs.
	var attachSucceeded bool
	defer func() {
		if movedToContainer && !attachSucceeded {
			if orphan, err := netlink.LinkByName(guestEnd); err == nil {
				_ = netlink.LinkDel(orphan)
			}
		}
	}()

	containerLink, err := netlink.LinkByName(guestEnd)
	if err != nil {
		return fmt.Errorf("find link after move: %w", err)
	}

	if lo, err := netlink.LinkByName("lo"); err == nil {
		_ = netlink.LinkSetUp(lo)
	}

	target := targetName
	if target == "" {
		target = "eth0"
	}

	if err := netlink.LinkSetDown(containerLink); err != nil {
		return fmt.Errorf("set link down before rename: %w", err)
	}
	if err := netlink.LinkSetName(containerLink, target); err != nil {
		return fmt.Errorf("rename to %s: %w", target, err)
	}

	containerLink, err = netlink.LinkByName(target)
	if err != nil {
		return err
	}
	if mac != "" {
		hardwareAddress, err := net.ParseMAC(mac)
		if err != nil {
			return fmt.Errorf("parse MAC %s: %w", mac, err)
		}
		if err := netlink.LinkSetHardwareAddr(containerLink, hardwareAddress); err != nil {
			return fmt.Errorf("assign MAC: %w", err)
		}
	}

	addr, err := netlink.ParseAddr(ip)
	if err != nil {
		return fmt.Errorf("parse IP %s: %w", ip, err)
	}
	if err := netlink.AddrAdd(containerLink, addr); err != nil {
		return fmt.Errorf("assign IP: %w", err)
	}

	if err := netlink.LinkSetUp(containerLink); err != nil {
		return err
	}

	if gateway != "" {
		_, defaultNet, _ := net.ParseCIDR("0.0.0.0/0")
		gwIP := net.ParseIP(gateway)
		route := &netlink.Route{
			LinkIndex: containerLink.Attrs().Index,
			Dst:       defaultNet,
			Gw:        gwIP,
		}
		if err := netlink.RouteReplace(route); err != nil {
			return fmt.Errorf("add default route: %w", err)
		}
	}

	attachSucceeded = true
	return nil
}
