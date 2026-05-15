package docker

import (
	"context"
	"fmt"
	"net"
	"runtime"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/oslab/sysbox/pkg/substrate"
)

// AttachNIC moves the guest-end of a veth pair into the container's netns,
// renames it to eth<N>, assigns the IP, and sets the default gateway.
//
// Assumptions:
//   - nic.GuestEnd already exists in the host-side netns where it was created
//     (the network provider's netns), so we first move it to the global root
//     netns, then into the container's netns. To keep things simple in Phase
//     1 we rely on netns.GetFromName to enter the network's netns, move the
//     link to the container PID, then enter the container netns and configure.
//   - container is running (StartNode completed)
//   - nic.Kind == "veth"
func (s *Substrate) AttachNIC(ctx context.Context, h substrate.NodeHandle, nic substrate.NIC) error {
	if nic.Kind != "veth" {
		return fmt.Errorf("docker substrate only supports veth, got %q", nic.Kind)
	}

	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	containerPID := ins.State.Pid
	if containerPID == 0 {
		return fmt.Errorf("container %s is not running", h.ID)
	}

	netnsName, _ := h.Attributes["network_netns"].(string)
	return attachVethToContainer(nic, netnsName, containerPID)
}

func attachVethToContainer(nic substrate.NIC, sourceNetnsName string, containerPID int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNS, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get root netns: %w", err)
	}
	defer origNS.Close()
	defer func() { _ = netns.Set(origNS) }()

	// Step 1: enter the netns where the guest-end currently lives.
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

	// Step 2: find the guest-end link.
	link, err := netlink.LinkByName(nic.GuestEnd)
	if err != nil {
		return fmt.Errorf("find veth guest end %s: %w", nic.GuestEnd, err)
	}

	// Step 3: move it directly into the container's netns by PID.
	if err := netlink.LinkSetNsPid(link, containerPID); err != nil {
		return fmt.Errorf("move veth to container netns: %w", err)
	}

	// Step 4: enter container's netns and configure the link.
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

	containerLink, err := netlink.LinkByName(nic.GuestEnd)
	if err != nil {
		return fmt.Errorf("find link after move: %w", err)
	}

	if lo, err := netlink.LinkByName("lo"); err == nil {
		_ = netlink.LinkSetUp(lo)
	}

	target := nic.TargetName
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

	addr, err := netlink.ParseAddr(nic.IP)
	if err != nil {
		return fmt.Errorf("parse IP %s: %w", nic.IP, err)
	}
	if err := netlink.AddrAdd(containerLink, addr); err != nil {
		return fmt.Errorf("assign IP: %w", err)
	}

	if err := netlink.LinkSetUp(containerLink); err != nil {
		return err
	}

	if nic.Gateway != "" {
		_, defaultNet, _ := net.ParseCIDR("0.0.0.0/0")
		gwIP := net.ParseIP(nic.Gateway)
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
