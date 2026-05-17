package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	providerexec "github.com/oslab/sysbox/pkg/provider/exec"
	firecrackerprovider "github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

func (e *Executor) createNode(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.NodeConfig)
	if !ok {
		return fmt.Errorf("node %s: wrong data type", n.ID)
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

	// Resolve sysbox_kernel references inside the substrate's typed
	// provider config (only firecracker uses this today). We rewrite the
	// reference to an absolute path so substrate.CreateNode receives a
	// ready-to-use value.
	if err := resolveProviderKernel(e.state, cfg.ProviderConfig); err != nil {
		return err
	}

	// Pre-scan links: collect Docker NAT networks so the first one can be
	// attached at container-creation time (keeping NetworkMode:"none" for
	// pure-veth nodes, and avoiding the post-start connect restriction).
	type natLink struct {
		netName string
		netID   string
		ip      string
	}
	var natLinks []natLink
	for _, link := range cfg.Links {
		netName := config.ResolveName(link.Network)
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			return fmt.Errorf("network %s not applied yet", netName)
		}
		if isNAT, _ := netState.Instance["nat"].(bool); isNAT {
			natLinks = append(natLinks, natLink{
				netName: netName,
				netID:   util.AsString(netState.Instance["docker_network_id"]),
				ip:      link.IP,
			})
		}
	}

	// Build initial Docker network attachments (first NAT link goes at create time).
	var initialNets []substrate.DockerNetworkAttachment
	for _, nl := range natLinks {
		initialNets = append(initialNets, substrate.DockerNetworkAttachment{
			NetworkID: nl.netID,
			IPv4:      nl.ip,
		})
	}

	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:              fmt.Sprintf("sysbox-%s", n.ID.Name),
		Image:             imgRef,
		VCPUs:             cfg.Vcpus,
		Memory:            cfg.Memory,
		Env:               cfg.Env,
		InitialDockerNets: initialNets,
		ProviderConfig:    cfg.ProviderConfig,
	})
	if err != nil {
		return err
	}

	// Docker containers must be started BEFORE AttachNIC so the container
	// has a PID and a network namespace to inject veths into. Firecracker
	// VMs must NOT be started yet — they need all TAPs declared in the
	// boot config before launch.
	// TODO(W1-PR-05): replace substrate-name check with Capabilities.NICHotPlug.
	if subName != "firecracker" {
		if err := sub.StartNode(ctx, handle); err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return fmt.Errorf("start node %s: %w", n.ID.Name, err)
		}
	}

	// NOTE: Do NOT call StartNode yet — Firecracker needs all NICs declared
	// in the boot config before launch. We call StartNode AFTER the NIC loop.

	// Track which NAT networks were already connected at create time.
	connectedAtCreate := map[string]bool{}
	if len(initialNets) > 0 {
		connectedAtCreate[initialNets[0].NetworkID] = true
	}

	nics := []map[string]any{}
	// vethIdx tracks the guest interface name for manually-injected veth links.
	// Docker NAT networks consume ethN names starting at eth0, so veth links
	// must begin numbering after however many NAT interfaces were attached.
	vethIdx := len(initialNets)
	for _, link := range cfg.Links {
		netName := config.ResolveName(link.Network)
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			_ = sub.DestroyNode(ctx, handle)
			return fmt.Errorf("network %s not applied yet", netName)
		}

		// NAT network: connected at create time (first) or via docker network connect (extras).
		if isNAT, _ := netState.Instance["nat"].(bool); isNAT {
			netID := util.AsString(netState.Instance["docker_network_id"])
			if !connectedAtCreate[netID] {
				dockerCap, err := e.dockerSubstrate()
				if err != nil {
					_ = sub.DestroyNode(ctx, handle)
					return err
				}
				if err := dockerCap.ConnectContainerToNetwork(ctx, handle.ID, netID, link.IP); err != nil {
					_ = sub.DestroyNode(ctx, handle)
					return fmt.Errorf("connect node %s to nat network %s: %w", n.ID.Name, netName, err)
				}
			}
			nics = append(nics, map[string]any{
				"type":       "docker_nat",
				"network_id": netID,
				"ip":         link.IP,
			})
			continue
		}

		// Non-NAT (isolated) network: delegate NIC creation to the substrate.
		lreq := substrate.LinkRequest{
			NetNS:      util.AsString(netState.Instance["netns"]),
			Bridge:     util.AsString(netState.Instance["bridge"]),
			IP:         link.IP,
			Gateway:    link.Gateway,
			TargetName: fmt.Sprintf("eth%d", vethIdx),
			MAC:        "",
		}
		attached, err := sub.AttachNIC(ctx, handle, lreq)
		if err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return err
		}
		vethIdx++
		nics = append(nics, map[string]any{
			"kind":      attached.Kind,
			"host_end":  attached.HostEnd,
			"guest_end": attached.GuestEnd,
			"target":    lreq.TargetName,
			"ip":        attached.IP,
			"netns":     attached.NetNS,
		})
	}

	nodeInstance := map[string]any{
		"container_id": handle.ID,
		"nics":         nics,
	}
	// Substrate-specific state (vsock metadata, vm_dir, etc.) goes through
	// MarshalProviderState so runtime stays substrate-agnostic.
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		nodeInstance["provider_extra"] = string(blob)
	}
	e.state.AddResource(state.Resource{
		Type:     "sysbox_node",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: nodeInstance,
	})

	// Start the node now that all NICs are attached.
	// Docker nodes were already started above (before AttachNIC).
	// For Firecracker this launches the VM with the complete config.
	if err := sub.StartNode(ctx, handle); err != nil {
		_ = sub.DestroyNode(ctx, handle)
		return fmt.Errorf("start node %s: %w", n.ID.Name, err)
	}

	// Populate the substrate-neutral PrimaryIP from the first link so the
	// Connection factory (W1-PR-06) can derive SSH coordinates uniformly.
	if len(cfg.Links) > 0 {
		firstIP := cfg.Links[0].IP
		if idx := strings.Index(firstIP, "/"); idx >= 0 {
			firstIP = firstIP[:idx]
		}
		handle.Net.PrimaryIP = firstIP
	}

	// SSH fallback for firecracker VMs whose rootfs lacks sysbox-init: write
	// the first link IP and configured SSH port into the typed HandleState.
	// W1-PR-06 will replace this branch with a substrate.Connection() factory.
	if fcCfg, ok := cfg.ProviderConfig.(*firecrackerprovider.Config); ok && fcCfg != nil {
		if hs, ok := handle.Provider.(*firecrackerprovider.HandleState); ok && hs != nil {
			hs.SSHIP = handle.Net.PrimaryIP
			port := fcCfg.SSHPort
			if port == 0 {
				port = 22
			}
			hs.SSHPort = fmt.Sprintf("%d", port)
		}
	}

	// Re-marshal provider state if mutated above (firecracker SSH fallback).
	if subName == "firecracker" {
		if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
			// Update the just-persisted node resource with the refreshed blob.
			if rec := e.state.FindResource("sysbox_node", n.ID.Name); rec != nil {
				rec.Instance["provider_extra"] = string(blob)
			}
		}
	}

	// Run provisioners after node is up and wired.
	if len(cfg.Provisioners) > 0 {
		conn := e.connectionForNode(sub, subName, handle, cfg.Connections)
		// Block until the chosen connection is reachable.
		switch c := conn.(type) {
		case *providerexec.SSHConnection:
			if c != nil {
				fmt.Printf("[provisioner] waiting for SSH on %s...\n", c.Host())
				if err := c.WaitForSSH(ctx, 60*time.Second); err != nil {
					return fmt.Errorf("ssh not ready on node %s: %w", n.ID.Name, err)
				}
			}
		case *providerexec.VsockConnection:
			if c != nil {
				fmt.Printf("[provisioner] waiting for vsock-agent on %s...\n", n.ID.Name)
				if err := c.WaitReady(ctx, 60*time.Second); err != nil {
					return fmt.Errorf("vsock-agent not ready on node %s: %w", n.ID.Name, err)
				}
			}
		}
		if err := e.runProvisioners(ctx, conn, cfg.Provisioners); err != nil {
			return fmt.Errorf("provisioner on node %s: %w", n.ID.Name, err)
		}
	}

	return nil
}

func (e *Executor) destroyNode(ctx context.Context, r state.Resource) error {
	sub, err := substrate.Get(r.Provider)
	if err != nil {
		return err
	}
	handle := substrate.NodeHandle{ID: r.Str("container_id")}
	// Reconstruct provider-specific state (vm_dir, vsock metadata, etc.) so
	// cold destroys after a process restart can clean up properly.
	if blob := r.Str("provider_extra"); blob != "" {
		if p, err := sub.UnmarshalProviderState([]byte(blob)); err == nil {
			handle.Provider = p
		}
	}
	// Ignore stop/destroy errors: container may already be gone (drift recovery).
	if err := sub.StopNode(ctx, handle); err != nil {
		fmt.Printf("[destroy] warning: stop node %s: %v\n", r.Name, err)
	}
	if err := sub.DestroyNode(ctx, handle); err != nil {
		fmt.Printf("[destroy] warning: destroy node %s: %v\n", r.Name, err)
	}
	// Always clean up veths/taps and state regardless of container presence.
	if nics, ok := r.Instance["nics"].([]any); ok {
		for _, item := range nics {
			n, _ := item.(map[string]any)
			kind := util.AsString(n["kind"])
			hostEnd := util.AsString(n["host_end"])
			nsName := util.AsString(n["netns"])
			if kind == "tap" {
				if err := network.DeleteTapDevice(hostEnd, nsName); err != nil {
					fmt.Printf("[destroy] warning: delete tap %s: %v\n", hostEnd, err)
				}
			} else {
				if err := network.DeleteVethPair(network.VethHandle{HostEnd: hostEnd, NetnsName: nsName}); err != nil {
					fmt.Printf("[destroy] warning: delete veth %s: %v\n", hostEnd, err)
				}
			}
		}
	}
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- provisioners --

// connectionForNode picks the right Connection implementation based on the
// substrate type and the optional connection block in the node config.
func (e *Executor) connectionForNode(
	sub substrate.Substrate,
	subName string,
	handle substrate.NodeHandle,
	conns []config.ConnectionConfig,
) providerexec.Connection {
	// Determine requested type (default: "auto").
	connType := "auto"
	if len(conns) > 0 && conns[0].Type != "" {
		connType = conns[0].Type
	}

	switch connType {
	case "auto":
		// Auto-select based on substrate.
		if dockerSub, ok := sub.(*dockerprovider.Substrate); ok {
			return providerexec.NewDockerConnection(dockerSub, handle)
		}
		// TODO(W1-PR-06): replace with Substrate.Connection(handle, hint)
		// factory; runtime stops switching on substrate names.
		if subName == "firecracker" {
			// Prefer vsock (no SSH dependency on the rootfs). Fall back to
			// SSH only if the handle has no vsock UDS attached (e.g. when
			// sysbox-init was disabled because the embed binary is missing).
			if c := vsockConnectionFromHandle(handle); c != nil {
				return c
			}
			return e.sshConnectionFromHandle(handle, conns)
		}
	case "docker":
		if dockerSub, ok := sub.(*dockerprovider.Substrate); ok {
			return providerexec.NewDockerConnection(dockerSub, handle)
		}
	case "ssh":
		return e.sshConnectionFromHandle(handle, conns)
	case "vsock":
		if c := vsockConnectionFromHandle(handle); c != nil {
			return c
		}
		return nil
	}

	// Fallback to docker if substrate supports it.
	if dockerSub, ok := sub.(*dockerprovider.Substrate); ok {
		return providerexec.NewDockerConnection(dockerSub, handle)
	}
	return nil
}

// vsockConnectionFromHandle builds a Vsock connection from the node handle.
// Returns nil if the handle does not advertise a vsock UDS (e.g. firecracker
// with sysbox-init disabled, or any non-firecracker substrate).
func vsockConnectionFromHandle(handle substrate.NodeHandle) *providerexec.VsockConnection {
	hs, _ := handle.Provider.(*firecrackerprovider.HandleState)
	if hs == nil || hs.VsockUDS == "" {
		return nil
	}
	return providerexec.NewVsockConnection(hs.VsockUDS, hs.VsockPort)
}

// sshConnectionFromHandle builds an SSH connection from the node handle.
// For firecracker the SSH coordinates live in the typed HandleState; for
// other substrates the connection block must supply Host explicitly.
func (e *Executor) sshConnectionFromHandle(handle substrate.NodeHandle, conns []config.ConnectionConfig) providerexec.Connection {
	host := handle.Net.PrimaryIP
	port := ""
	if hs, _ := handle.Provider.(*firecrackerprovider.HandleState); hs != nil {
		if hs.SSHIP != "" {
			host = hs.SSHIP
		}
		port = hs.SSHPort
	}
	user := "root"
	pass := "root"
	key := ""

	if len(conns) > 0 {
		c := conns[0]
		if c.Host != "" {
			host = c.Host
		}
		if c.User != "" {
			user = c.User
		}
		if c.Password != "" {
			pass = c.Password
		}
		if c.PrivateKey != "" {
			key = c.PrivateKey
		}
	}

	if host == "" {
		return nil
	}
	return providerexec.NewSSHConnectionWithPort(host, port, user, key, pass)
}

// runProvisioners executes provisioner blocks in order.
func (e *Executor) runProvisioners(ctx context.Context, conn providerexec.Connection, provs []config.ProvisionerConfig) error {
	if conn == nil {
		return fmt.Errorf("no connection available for provisioners")
	}
	for _, p := range provs {
		switch p.Type {
		case "exec":
			if len(p.Inline) == 0 {
				continue
			}
			if p.Background {
				cmd := []string{"sh", "-c", strings.Join(p.Inline, " && ")}
				pid, err := conn.ExecBackground(ctx, cmd, nil)
				if err != nil {
					return fmt.Errorf("provisioner exec (background): %w", err)
				}
				fmt.Printf("[provisioner] background exec started (pid %d)\n", pid)
			} else {
				fmt.Printf("[provisioner] exec: %v\n", p.Inline)
				if err := conn.ExecInline(ctx, p.Inline); err != nil {
					return err
				}
			}
		case "file":
			if p.Source == "" || p.Destination == "" {
				return fmt.Errorf("provisioner file: source and destination required")
			}
			src := expandTilde(p.Source)
			fmt.Printf("[provisioner] file: %s → %s\n", src, p.Destination)
			if err := conn.CopyFile(ctx, src, p.Destination); err != nil {
				return fmt.Errorf("provisioner file %s: %w", src, err)
			}
		default:
			return fmt.Errorf("unknown provisioner type %q", p.Type)
		}
	}
	return nil
}

// resolveProviderKernel rewrites firecracker provider Config.Kernel from a
// sysbox_kernel resource reference to an absolute filesystem path by looking
// up the resolved path in state. Literal paths are left untouched. Substrates
// other than firecracker have no kernel field and are skipped.
//
// W1-PR-05 generalises this into a substrate.Substrate.ResolveRefs hook so
// runtime stops dispatching on concrete config types here.
func resolveProviderKernel(st *state.State, pc any) error {
	fcCfg, ok := pc.(*firecrackerprovider.Config)
	if !ok || fcCfg == nil {
		return nil
	}
	if fcCfg.Kernel == "" || !config.LooksLikeKernelRef(fcCfg.Kernel) {
		return nil
	}
	kname := config.ResolveName(fcCfg.Kernel)
	kState := st.FindResource("sysbox_kernel", kname)
	if kState == nil {
		return fmt.Errorf("kernel %s not applied yet", kname)
	}
	resolved := util.AsString(kState.Instance["path"])
	if resolved == "" {
		return fmt.Errorf("kernel %s has no resolved path in state", kname)
	}
	fcCfg.Kernel = resolved
	return nil
}
