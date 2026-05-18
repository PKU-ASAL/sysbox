package docker

import (
	"context"
	"fmt"
	"net"
	"runtime"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// AttachNIC creates a network attachment for the container. Two paths:
//   - KindHint == NICKindDockerNAT (or DockerNetID != ""): uses
//     docker network connect — the Docker daemon manages the veth/bridge.
//   - Otherwise: creates a veth pair, wires into isolated netns bridge,
//     moves guest-end into container netns (the "cold-plug" path).
func (s *Substrate) AttachNIC(ctx context.Context, h substrate.NodeHandle, req substrate.LinkRequest) (substrate.AttachedNIC, error) {
	// Docker NAT bridge: delegate to docker network connect.
	if req.DockerNetID != "" || req.KindHint == substrate.NICKindDockerNAT {
		if err := s.ConnectContainerToNetwork(ctx, h.ID, req.DockerNetID, req.IP); err != nil {
			return substrate.AttachedNIC{}, fmt.Errorf("docker network connect: %w", err)
		}
		return substrate.AttachedNIC{
			Kind: substrate.NICKindDockerNAT,
			IP:   req.IP,
		}, nil
	}

	// Isolated network: veth pair + netns injection.
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return substrate.AttachedNIC{}, fmt.Errorf("inspect container: %w", err)
	}
	containerPID := ins.State.Pid
	if containerPID == 0 {
		return substrate.AttachedNIC{}, fmt.Errorf("container %s is not running", h.ID)
	}

	hs, _ := h.Provider.(*HandleState)
	containerName := "sysbox-unknown"
	if hs != nil && hs.ContainerName != "" {
		containerName = hs.ContainerName
	}

	// Derive deterministic veth names from the container name so leftover
	// cleanup on retry works reliably.
	hostEnd := vethName("vh", containerName)
	guestEnd := vethName("vg", containerName)

	// Create the veth pair inside the network's netns and attach the
	// host-end to the bridge. Idempotent: reuses existing devices.
	pair, err := network.CreateVethPair(network.VethSpec{
		HostEnd:    hostEnd,
		GuestEnd:   guestEnd,
		NetnsName:  req.NetNS,
		BridgeName: req.Bridge,
	})
	if err != nil {
		return substrate.AttachedNIC{}, fmt.Errorf("create veth pair: %w", err)
	}

	// Move the guest-end into the container's netns and configure it.
	if err := attachVethToContainer(guestEnd, req.TargetName, req.NetNS, containerPID, req.IP, req.Gateway); err != nil {
		return substrate.AttachedNIC{}, err
	}

	return substrate.AttachedNIC{
		Kind:     substrate.NICKindVeth,
		HostEnd:  pair.HostEnd,
		GuestEnd: pair.GuestEnd,
		IP:       req.IP,
		NetNS:    req.NetNS,
	}, nil
}

// vethName generates a deterministic veth interface name from a prefix and
// container name. Names must be ≤15 chars.
func vethName(prefix, containerName string) string {
	name := containerName
	if len(name) > 8 && name[:8] == "sysbox-" {
		name = name[8:]
	}
	vn := prefix + "-" + name
	if len(vn) > 15 {
		vn = vn[:15]
	}
	return vn
}

// attachVethToContainer moves a guest-end veth from sourceNetns into the
// container's netns (identified by PID), renames it, assigns IP, and
// optionally sets the default gateway.
func attachVethToContainer(guestEnd, targetName, sourceNetnsName string, containerPID int, ip, gateway string) error {
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

	return nil
}
