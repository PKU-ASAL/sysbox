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

	// Resolve cross-resource refs (e.g. kernel ref → local path) before CreateNode.
	// We do an early PrepareHandle pass with an empty handle (no PrimaryIP yet)
	// purely for ref resolution. ConnInfo is populated in the second pass below.
	if err := sub.PrepareHandle(ctx, &substrate.NodeHandle{}, cfg.ProviderConfig, stateAdapter{e.state}); err != nil {
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

	// Build InitialLinks for NAT networks (substrate connects at create time).
	var initialLinks []substrate.LinkRequest
	for _, nl := range natLinks {
		initialLinks = append(initialLinks, substrate.LinkRequest{
			KindHint:    substrate.NICKindDockerNAT,
			DockerNetID: nl.netID,
			IP:          nl.ip,
		})
	}

	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:           fmt.Sprintf("sysbox-%s", n.ID.Name),
		Image:          imgRef,
		VCPUs:          cfg.Vcpus,
		Memory:         cfg.Memory,
		Env:            cfg.Env,
		InitialLinks:   initialLinks,
		ProviderConfig: cfg.ProviderConfig,
	})
	if err != nil {
		return err
	}

	// Start-node ordering is driven by the substrate's capabilities:
	//   NICHotPlug=true  (docker):  start first, then AttachNIC injects
	//                   veths into the running container's netns.
	//   NICHotPlug=false (FC/VM):  attach NICs first (they must be in the
	//                   boot config), then start the VM.
	caps := sub.Capabilities()
	if caps.NICHotPlug {
		if err := sub.StartNode(ctx, handle); err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return fmt.Errorf("start node %s: %w", n.ID.Name, err)
		}
	}

	// Track which NAT networks were already connected at create time.
	connectedAtCreate := map[string]bool{}
	if len(initialLinks) > 0 {
		connectedAtCreate[initialLinks[0].DockerNetID] = true
	}

	nics := []map[string]any{}
	// vethIdx tracks the guest interface name for manually-injected veth links.
	// Docker NAT networks consume ethN names starting at eth0, so veth links
	// must begin numbering after however many NAT interfaces were attached.
	vethIdx := len(initialLinks)
	for _, link := range cfg.Links {
		netName := config.ResolveName(link.Network)
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			_ = sub.DestroyNode(ctx, handle)
			return fmt.Errorf("network %s not applied yet", netName)
		}

		// NAT network: connected at create time (first) or via AttachNIC (extras).
		if isNAT, _ := netState.Instance["nat"].(bool); isNAT {
			netID := util.AsString(netState.Instance["docker_network_id"])
			if !connectedAtCreate[netID] {
				_, err := sub.AttachNIC(ctx, handle, substrate.LinkRequest{
					KindHint:    substrate.NICKindDockerNAT,
					DockerNetID: netID,
					IP:          link.IP,
				})
				if err != nil {
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

	// Populate PrimaryIP from the first link before writing state, so that
	// actor resources can read it back from nodeState.Instance["primary_ip"].
	if len(cfg.Links) > 0 {
		firstIP := cfg.Links[0].IP
		if idx := strings.Index(firstIP, "/"); idx >= 0 {
			firstIP = firstIP[:idx]
		}
		handle.Net.PrimaryIP = firstIP
	}

	nodeInstance := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   handle.Net.PrimaryIP,
		"nics":         nics,
	}
	// Persist lifecycle flags so ComputePlan can honour them on future runs
	// even if the resource is removed from HCL.
	if lc := cfg.Lifecycle; lc != nil {
		nodeInstance["lifecycle_prevent_destroy"] = lc.PreventDestroy
		if len(lc.IgnoreChanges) > 0 {
			nodeInstance["lifecycle_ignore_changes"] = lc.IgnoreChanges
		}
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

	// Cold-plug substrates (NICHotPlug=false) start the node AFTER all NICs
	// are attached (NICs must be in the boot config). Hot-plug substrates
	// were already started before the NIC loop.
	if !caps.NICHotPlug {
		if err := sub.StartNode(ctx, handle); err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return fmt.Errorf("start node %s: %w", n.ID.Name, err)
		}
	}

	// Let the substrate populate ConnInfo (Kind, Endpoint, Auth) now that
	// PrimaryIP is set. Each substrate decides what makes sense:
	// docker → ConnKindDocker (set at CreateNode), FC → vsock or SSH.
	if err := sub.PrepareHandle(ctx, &handle, cfg.ProviderConfig, stateAdapter{e.state}); err != nil {
		e.logf("[apply] warning: PrepareHandle for %s: %v\n", n.ID.Name, err)
	}

	// Re-marshal provider state (the substrate may have mutated HandleState
	// during AttachNIC or PrepareHandle). Always try; substrates with no
	// provider state return (nil, nil) which is harmless.
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		if rec := e.state.FindResource("sysbox_node", n.ID.Name); rec != nil {
			rec.Instance["provider_extra"] = string(blob)
		}
	}

	// Configure static routes declared in HCL (before provisioners so they
	// can use the routes). This replaces `ip route add` in provisioners.
	if len(cfg.Routes) > 0 {
		conn, err := connectionForNode(sub, handle, cfg.Connections)
		if err != nil {
			return fmt.Errorf("connection for routes on node %s: %w", n.ID.Name, err)
		}
		for _, rt := range cfg.Routes {
			cmd := fmt.Sprintf("ip route add %s via %s", rt.Destination, rt.Via)
			e.logf("[route] %s: %s\n", n.ID.Name, cmd)
			if err := conn.ExecInline(ctx, []string{cmd}); err != nil {
				// Non-fatal: route may already exist or ip not available.
				e.logf("[route] warning: %s: %v\n", n.ID.Name, err)
			}
		}
		// Persist routes in state for drift detection.
		routeSpecs := make([]map[string]string, 0, len(cfg.Routes))
		for _, rt := range cfg.Routes {
			routeSpecs = append(routeSpecs, map[string]string{"dst": rt.Destination, "via": rt.Via})
		}
		if rec := e.state.FindResource("sysbox_node", n.ID.Name); rec != nil {
			rec.Instance["routes"] = routeSpecs
		}
	}

	// Run provisioners after node is up and wired.
	if len(cfg.Provisioners) > 0 {
		conn, err := connectionForNode(sub, handle, cfg.Connections)
		if err != nil {
			return fmt.Errorf("connection for node %s: %w", n.ID.Name, err)
		}
		// Block until the chosen connection is reachable.
		switch c := conn.(type) {
		case *providerexec.SSHConnection:
			if c != nil {
				e.logf("[provisioner] waiting for SSH on %s...\n", c.Host())
				if err := c.WaitForSSH(ctx, 60*time.Second); err != nil {
					return fmt.Errorf("ssh not ready on node %s: %w", n.ID.Name, err)
				}
			}
		case *providerexec.VsockConnection:
			if c != nil {
				e.logf("[provisioner] waiting for vsock-agent on %s...\n", n.ID.Name)
				if err := c.WaitReady(ctx, 60*time.Second); err != nil {
					return fmt.Errorf("vsock-agent not ready on node %s: %w", n.ID.Name, err)
				}
			}
		}
		if err := e.runProvisioners(ctx, conn, cfg.Provisioners); err != nil {
			return fmt.Errorf("provisioner on node %s: %w", n.ID.Name, err)
		}
	}

	// For Docker nodes, launch the image's original CMD/Entrypoint inside
	// the container (we overrode it with "sleep infinity" during CreateNode).
	if err := e.execImageEntry(ctx, handle, subName); err != nil {
		e.logf("[node] warning: image entry start: %v\n", err)
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
		e.logf("[destroy] warning: stop node %s: %v\n", r.Name, err)
	}
	if err := sub.DestroyNode(ctx, handle); err != nil {
		e.logf("[destroy] warning: destroy node %s: %v\n", r.Name, err)
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
					e.logf("[destroy] warning: delete tap %s: %v\n", hostEnd, err)
				}
			} else {
				if err := network.DeleteVethPair(network.VethHandle{HostEnd: hostEnd, NetnsName: nsName}); err != nil {
					e.logf("[destroy] warning: delete veth %s: %v\n", hostEnd, err)
				}
			}
		}
	}
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- provisioners --

// connectionForNode picks the right Connection implementation based on the
// connectionForNode delegates to Substrate.Connection(). The substrate
// inspects NodeHandle.Conn and the optional HCL hints to pick the right
// implementation (docker-exec, vsock-rpc, SSH, ...).
func connectionForNode(
	sub substrate.Substrate,
	handle substrate.NodeHandle,
	conns []config.ConnectionConfig,
) (substrate.Connection, error) {
	hints := make([]substrate.ConnectionHint, len(conns))
	for i, c := range conns {
		hints[i] = substrate.ConnectionHint{
			Type:       c.Type,
			Host:       c.Host,
			User:       c.User,
			Password:   c.Password,
			PrivateKey: c.PrivateKey,
		}
	}
	return sub.Connection(handle, hints)
}

// runProvisioners executes provisioner blocks in order.
func (e *Executor) runProvisioners(ctx context.Context, conn substrate.Connection, provs []config.ProvisionerConfig) error {
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
				e.logf("[provisioner] background exec started (pid %d)\n", pid)
			} else {
				e.logf("[provisioner] exec: %v\n", p.Inline)
				if err := conn.ExecInline(ctx, p.Inline); err != nil {
					return err
				}
			}
		case "file":
			if p.Source == "" || p.Destination == "" {
				return fmt.Errorf("provisioner file: source and destination required")
			}
			src := expandTilde(p.Source)
			e.logf("[provisioner] file: %s → %s\n", src, p.Destination)
			if err := conn.CopyFile(ctx, src, p.Destination); err != nil {
				return fmt.Errorf("provisioner file %s: %w", src, err)
			}
		default:
			return fmt.Errorf("unknown provisioner type %q", p.Type)
		}
	}
	return nil
}

// execImageEntry launches the image's original CMD/Entrypoint inside a Docker
// container. During CreateNode we override with "sleep infinity" so provisioners
// can run; after provisioning we exec the original entrypoint so services
// (nginx, postgres, etc.) actually start.
func (e *Executor) execImageEntry(ctx context.Context, handle substrate.NodeHandle, subName string) error {
	if subName != "docker" {
		return nil // only Docker overrides the entrypoint
	}
	hs, ok := handle.Provider.(*dockerprovider.HandleState)
	if !ok || hs == nil {
		return nil
	}
	if len(hs.ImageEntrypoint) == 0 && len(hs.ImageCmd) == 0 {
		return nil // image has no entrypoint (e.g. alpine)
	}

	// Build the command: entrypoint + cmd, or just cmd
	cmd := make([]string, 0, len(hs.ImageEntrypoint)+len(hs.ImageCmd))
	cmd = append(cmd, hs.ImageEntrypoint...)
	cmd = append(cmd, hs.ImageCmd...)

	conn, err := substrate.Get(subName)
	if err != nil {
		return err
	}
	dsub := conn.(substrate.Substrate)
	c, err := dsub.Connection(handle, nil)
	if err != nil || c == nil {
		return fmt.Errorf("no connection to start image entry: %w", err)
	}

	e.logf("[node] starting image entry: %v\n", cmd)
	// Run as background process so it doesn't block the executor
	_, err = c.ExecBackground(ctx, cmd, nil)
	return err
}
