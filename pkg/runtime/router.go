package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// createRouter provisions a multi-NIC node with IP forwarding enabled.
// Interfaces on NAT (Docker-managed) networks are connected via Docker
// networking; isolated-network interfaces use veth pairs as usual.
// Optional NAT (nat_from -> nat_to) is configured via host-side nsenter.
func (e *Executor) createRouter(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.RouterConfig)
	if !ok {
		return fmt.Errorf("router %s: wrong data type", n.ID)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}

	imageName := config.ResolveName(cfg.Image)
	imgState := e.state.FindResource("sysbox_image", imageName)
	if imgState == nil {
		return fmt.Errorf("image %s not applied yet", imageName)
	}
	imgRef := substrate.ImageRef{
		ID:         util.AsString(imgState.Instance["image_id"]),
		Repository: util.AsString(imgState.Instance["repository"]),
	}

	// Pre-scan interfaces: find the first NAT network so it can be
	// connected at container-creation time (required for Docker to
	// assign the correct eth name and IP).
	var initialNets []substrate.DockerNetworkAttachment
	for _, iface := range cfg.Interfaces {
		netName := config.ResolveName(iface.Network)
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			return fmt.Errorf("network %s not applied yet", netName)
		}
		if isNAT, _ := netState.Instance["nat"].(bool); isNAT && len(initialNets) == 0 {
			netID := util.AsString(netState.Instance["docker_network_id"])
			initialNets = append(initialNets, substrate.DockerNetworkAttachment{
				NetworkID: netID,
				IPv4:      iface.IP,
			})
		}
	}

	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:              fmt.Sprintf("sysbox-%s", n.ID.Name),
		Image:             imgRef,
		Sysctls:           map[string]string{"net.ipv4.ip_forward": "1"},
		InitialDockerNets: initialNets,
	})
	if err != nil {
		return err
	}

	if err := sub.StartNode(ctx, handle); err != nil {
		_ = sub.DestroyNode(ctx, handle)
		return err
	}

	// Docker assigns eth names sequentially from eth0 for networks
	// connected at create time. Isolated-network veths are injected
	// after, so we must track the correct eth index.
	connectedAtCreate := map[string]bool{}
	if len(initialNets) > 0 {
		connectedAtCreate[initialNets[0].NetworkID] = true
	}
	natIdx := 0                // NAT interfaces: eth0, eth1, ... (Docker-assigned order)
	vethIdx := len(initialNets) // veth guest-iface starts after NAT ifaces

	nics := []map[string]any{}
	ifaceByName := map[string]string{} // logical name -> guest interface (eth0/eth1/...)
	dockerCap, _ := e.dockerSubstrate()

	for _, iface := range cfg.Interfaces {
		netName := config.ResolveName(iface.Network)
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			_ = sub.DestroyNode(ctx, handle)
			return fmt.Errorf("network %s not applied yet", netName)
		}

		// NAT network: already connected at create time, or connect now.
		if isNAT, _ := netState.Instance["nat"].(bool); isNAT {
			netID := util.AsString(netState.Instance["docker_network_id"])
			if !connectedAtCreate[netID] && dockerCap != nil {
				if err := dockerCap.ConnectContainerToNetwork(ctx, handle.ID, netID, iface.IP); err != nil {
					_ = sub.DestroyNode(ctx, handle)
					return fmt.Errorf("connect router %s to nat network %s: %w", n.ID.Name, netName, err)
				}
			}
			// NAT ifaces are numbered eth0, eth1, ... by Docker in
			// the order they are attached (first at create time, then
			// via network connect). Use a separate counter so the
			// mapping is correct regardless of HCL interface order.
			target := fmt.Sprintf("eth%d", natIdx)
			natIdx++
			ifaceByName[iface.Name] = target
			nics = append(nics, map[string]any{
				"type":       "docker_nat",
				"network_id": netID,
				"ip":         iface.IP,
				"target":     target,
				"label":      iface.Name,
			})
			continue
		}

		// Non-NAT (isolated) network: delegate NIC creation to the substrate.
		lreq := substrate.LinkRequest{
			NetNS:      util.AsString(netState.Instance["netns"]),
			Bridge:     util.AsString(netState.Instance["bridge"]),
			IP:         iface.IP,
			TargetName: fmt.Sprintf("eth%d", vethIdx),
		}
		attached, err := sub.AttachNIC(ctx, handle, lreq)
		if err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return err
		}
		vethIdx++
		ifaceByName[iface.Name] = lreq.TargetName
		nics = append(nics, map[string]any{
			"host_end":  attached.HostEnd,
			"guest_end": attached.GuestEnd,
			"target":    lreq.TargetName,
			"ip":        attached.IP,
			"netns":     attached.NetNS,
			"label":     iface.Name,
		})
	}

	natApplied := false
	if cfg.NatFrom != "" && cfg.NatTo != "" {
		fromIf, ok1 := ifaceByName[cfg.NatFrom]
		toIf, ok2 := ifaceByName[cfg.NatTo]
		if !ok1 || !ok2 {
			return fmt.Errorf("nat_from %q / nat_to %q must reference declared interfaces",
				cfg.NatFrom, cfg.NatTo)
		}
		if err := configureNATViaNsenter(handle.ID, fromIf, toIf); err != nil {
			fmt.Printf("[router %s] warning: NAT setup failed (continuing without NAT): %v\n", n.ID.Name, err)
		} else {
			natApplied = true
		}
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_router",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"container_id": handle.ID,
			"nics":         nics,
			"nat_applied":  natApplied,
		},
	})
	return nil
}

func (e *Executor) destroyRouter(ctx context.Context, r state.Resource) error {
	// destroyRouter mirrors destroyNode: stop+remove container, delete veths.
	return e.destroyNode(ctx, r)
}

// configureNATViaNsenter configures MASQUERADE and FORWARD rules from the
// host side using nsenter(1) to enter the container's network namespace.
// This avoids the need for iptables inside the container (which would
// require internet access to install via apk/apt-get, and DNS often
// doesn't work in fresh Alpine containers on the Docker default bridge).
//
// The host's iptables binary operates on the kernel's netfilter, which is
// per-network-namespace — so running it via nsenter -t <pid> -n targets
// exactly the right namespace.
func configureNATViaNsenter(containerID, fromIf, toIf string) error {
	// Resolve container PID.
	out, err := execCommand("docker", "inspect", containerID, "--format", "{{.State.Pid}}")
	if err != nil {
		return fmt.Errorf("docker inspect %s: %w", containerID, err)
	}
	pid := strings.TrimSpace(string(out))
	if pid == "0" {
		return fmt.Errorf("container %s not running (pid 0)", containerID)
	}

	// Check that nsenter + iptables are available on the host.
	if _, err := execCommand("nsenter", "--version"); err != nil {
		return fmt.Errorf("nsenter not found on host: %w", err)
	}

	cmds := []string{
		fmt.Sprintf("nsenter -t %s -n iptables -t nat -A POSTROUTING -o %s -j MASQUERADE", pid, toIf),
		fmt.Sprintf("nsenter -t %s -n iptables -A FORWARD -i %s -o %s -j ACCEPT", pid, fromIf, toIf),
		fmt.Sprintf("nsenter -t %s -n iptables -A FORWARD -i %s -o %s -m state --state ESTABLISHED,RELATED -j ACCEPT", pid, toIf, fromIf),
	}
	for _, c := range cmds {
		parts := strings.Fields(c)
		out, err := execCommand(parts[0], parts[1:]...)
		if err != nil {
			return fmt.Errorf("cmd %q: %w (%s)", c, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// execCommand is a small wrapper around exec.Command.CombinedOutput for
// running host-side commands. Extracted for testability.
var execCommand = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
