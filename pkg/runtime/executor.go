package runtime

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	providerexec "github.com/oslab/sysbox/pkg/provider/exec"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Executor wires graph walking to provider calls. It holds references to
// registered substrates (via substrate.Get) and updates state after each action.
type Executor struct {
	graph *graph.Graph
	state *state.State
}

func NewExecutor(g *graph.Graph, s *state.State) *Executor {
	return &Executor{graph: g, state: s}
}

// CreateResource dispatches a node in the graph to the right provider
// and records the result in state.
func (e *Executor) CreateResource(ctx context.Context, id graph.NodeID) error {
	node := e.graph.Get(id.Type, id.Name)
	if node == nil {
		return fmt.Errorf("node %s not in graph", id)
	}

	switch id.Type {
	case "sysbox_network":
		return e.createNetwork(ctx, node)
	case "sysbox_image":
		return e.createImage(ctx, node)
	case "sysbox_kernel":
		return e.createKernel(ctx, node)
	case "sysbox_node":
		return e.createNode(ctx, node)
	case "sysbox_router":
		return e.createRouter(ctx, node)
	case "sysbox_firewall":
		return e.createFirewall(ctx, node)
	case "sysbox_ssh_access":
		return e.createSSHAccess(ctx, node)
	case "sysbox_agent":
		return e.createAgent(ctx, node)
	case "sysbox_actor":
		return e.createActor(ctx, node)
	case "sysbox_monitor":
		return e.createMonitor(ctx, node)
	default:
		return nil
	}
}

// DestroyResource tears down a resource listed in state.
func (e *Executor) DestroyResource(ctx context.Context, r state.Resource) error {
	switch r.Type {
	case "sysbox_network":
		return e.destroyNetwork(ctx, r)
	case "sysbox_node":
		return e.destroyNode(ctx, r)
	case "sysbox_router":
		return e.destroyRouter(ctx, r)
	case "sysbox_image":
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	case "sysbox_kernel":
		// Cache files are content-addressed and shared; do not delete from disk.
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	case "sysbox_firewall":
		return e.destroyFirewall(ctx, r)
	case "sysbox_ssh_access":
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	case "sysbox_agent":
		return e.destroyAgent(ctx, r)
	case "sysbox_actor":
		return e.destroyActor(ctx, r)
	case "sysbox_monitor":
		return e.destroyMonitor(r)
	default:
		fmt.Printf("[destroy] skipping unimplemented resource type %q (%s)\n", r.Type, r.Name)
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	}
}

// -- networks --

func (e *Executor) createNetwork(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.NetworkConfig)
	if !ok {
		return fmt.Errorf("network %s: wrong data type", n.ID)
	}

	// nat=true: use Docker's bridge driver for internet access.
	if cfg.NAT {
		return e.createNATNetwork(ctx, n, cfg)
	}

	// Default: isolated netns/bridge/veth topology.
	nsName := fmt.Sprintf("sysbox-net-%s", n.ID.Name)
	if err := network.CreateNetns(nsName); err != nil {
		return err
	}

	brName := fmt.Sprintf("br-%s", n.ID.Name)
	gwCIDR, err := network.GatewayCIDR(cfg.CIDR)
	if err != nil {
		return err
	}
	if err := network.CreateBridge(network.BridgeConfig{
		NetnsName: nsName, BridgeName: brName, CIDR: gwCIDR,
	}); err != nil {
		return err
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_network",
		Name:     n.ID.Name,
		Provider: "network",
		Instance: map[string]any{
			"netns":   nsName,
			"bridge":  brName,
			"cidr":    cfg.CIDR,
			"gateway": gwCIDR,
		},
	})
	return nil
}

// createNATNetwork creates a Docker-native bridge network for internet access.
func (e *Executor) createNATNetwork(ctx context.Context, n *graph.Node, cfg *config.NetworkConfig) error {
	dockerSub, err := e.dockerSubstrate()
	if err != nil {
		return fmt.Errorf("nat network requires docker substrate: %w", err)
	}

	netName := fmt.Sprintf("sysbox-nat-%s", n.ID.Name)
	netID, err := dockerSub.CreateBridgeNetwork(ctx, netName, cfg.CIDR)
	if err != nil {
		return fmt.Errorf("create nat network %s: %w", n.ID.Name, err)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_network",
		Name:     n.ID.Name,
		Provider: "docker",
		Instance: map[string]any{
			"nat":               true,
			"docker_network_id": netID,
			"docker_net_name":   netName,
			"cidr":              cfg.CIDR,
		},
	})
	return nil
}

func (e *Executor) destroyNetwork(ctx context.Context, r state.Resource) error {
	if isNAT, _ := r.Instance["nat"].(bool); isNAT {
		dockerSub, err := e.dockerSubstrate()
		if err != nil {
			e.state.RemoveResource(r.Type, r.Name)
			return nil
		}
		netID := asString(r.Instance["docker_network_id"])
		if netID != "" {
			_ = dockerSub.RemoveBridgeNetwork(ctx, netID)
		}
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	}

	nsName, _ := r.Instance["netns"].(string)
	brName, _ := r.Instance["bridge"].(string)
	_ = network.DeleteBridge(network.BridgeConfig{NetnsName: nsName, BridgeName: brName})
	_ = network.DeleteNetns(nsName)
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- images --

func (e *Executor) createImage(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.ImageConfig)
	if !ok {
		return fmt.Errorf("image %s: wrong data type", n.ID)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}

	// Resolve rootfs source through the artifact resolver. This makes
	// URL-based rootfs identical to local-path rootfs from the substrate's
	// perspective: the substrate always sees an absolute local path.
	rootfs := cfg.Rootfs
	var rootfsSHA string
	if rootfs != "" {
		res, err := artifact.New().Resolve(artifact.Spec{Source: rootfs, SHA256: cfg.SHA256})
		if err != nil {
			return fmt.Errorf("image %s rootfs: %w", n.ID.Name, err)
		}
		if res.FromCache {
			fmt.Printf("[apply] image %s: rootfs cache hit (%s)\n", n.ID.Name, res.Path)
		} else if artifact.IsURL(cfg.Rootfs) {
			fmt.Printf("[apply] image %s: rootfs fetched to %s\n", n.ID.Name, res.Path)
		}
		rootfs = res.Path
		rootfsSHA = res.SHA256
	}

	ref, err := sub.PrepareImage(ctx, substrate.ImageSpec{
		DockerRef: cfg.DockerRef,
		Rootfs:    rootfs,
		Size:      cfg.Size,
	})
	if err != nil {
		return err
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_image",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"image_id":   ref.ID,
			"repository": ref.Repository,
			"source":     cfg.Rootfs,
			"sha256":     rootfsSHA,
		},
	})
	return nil
}

// -- kernels --

// createKernel resolves a sysbox_kernel resource into a local on-disk path
// via the artifact resolver (downloading + caching as needed) and records it
// in state. Other resources (sysbox_node) reference the resolved path by
// looking up state["sysbox_kernel", name].path.
func (e *Executor) createKernel(_ context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.KernelConfig)
	if !ok {
		return fmt.Errorf("kernel %s: wrong data type", n.ID)
	}
	if cfg.Source == "" {
		return fmt.Errorf("kernel %s: source required", n.ID.Name)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}

	res, err := artifact.New().Resolve(artifact.Spec{Source: cfg.Source, SHA256: cfg.SHA256})
	if err != nil {
		return fmt.Errorf("kernel %s: %w", n.ID.Name, err)
	}
	if res.FromCache {
		fmt.Printf("[apply] kernel %s: cache hit (%s)\n", n.ID.Name, res.Path)
	} else if artifact.IsURL(cfg.Source) {
		fmt.Printf("[apply] kernel %s: fetched to %s\n", n.ID.Name, res.Path)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_kernel",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"path":             res.Path,
			"source":           cfg.Source,
			"sha256":           res.SHA256,
			"cmdline_template": cfg.CmdlineTemplate,
		},
	})
	return nil
}

// -- nodes --

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

	imageName, err := resolveImageRef(cfg.Image)
	if err != nil {
		return err
	}
	imgState := e.state.FindResource("sysbox_image", imageName)
	if imgState == nil {
		return fmt.Errorf("image %s not applied yet", imageName)
	}
	imgRef := substrate.ImageRef{
		ID:         asString(imgState.Instance["image_id"]),
		Repository: asString(imgState.Instance["repository"]),
	}

	// Resolve the kernel field. Three legal forms:
	//   - empty                          : use the substrate's default kernel
	//   - "sysbox_kernel.NAME.id" / "NAME": look up the kernel in state
	//   - "/abs/path" or "rel/path"      : literal filesystem path
	kernelPath := cfg.Kernel
	if kernelPath != "" && looksLikeKernelReference(kernelPath) {
		kname, err := resolveKernelRef(kernelPath)
		if err != nil {
			return err
		}
		kState := e.state.FindResource("sysbox_kernel", kname)
		if kState == nil {
			return fmt.Errorf("kernel %s not applied yet", kname)
		}
		kernelPath = asString(kState.Instance["path"])
		if kernelPath == "" {
			return fmt.Errorf("kernel %s has no resolved path in state", kname)
		}
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
		netName, err := resolveNetworkRef(link.Network)
		if err != nil {
			return err
		}
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			return fmt.Errorf("network %s not applied yet", netName)
		}
		if isNAT, _ := netState.Instance["nat"].(bool); isNAT {
			natLinks = append(natLinks, natLink{
				netName: netName,
				netID:   asString(netState.Instance["docker_network_id"]),
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
		Kernel:            kernelPath,
		Rootfs:            cfg.Rootfs,
		SSHUser:           cfg.SSHUser,
		SSHPass:           cfg.SSHPass,
		SSHPort:           cfg.SSHPort,
		Env:               cfg.Env,
		Privileged:        cfg.Privileged,
		PidMode:           cfg.PidMode,
		CgroupnsMode:      cfg.CgroupnsMode,
		Binds:             cfg.Binds,
		InitialDockerNets: initialNets,
		ChainInit:         cfg.ChainInit,
	})
	if err != nil {
		return err
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
		netName, err := resolveNetworkRef(link.Network)
		if err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return err
		}
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			_ = sub.DestroyNode(ctx, handle)
			return fmt.Errorf("network %s not applied yet", netName)
		}

		// NAT network: connected at create time (first) or via docker network connect (extras).
		if isNAT, _ := netState.Instance["nat"].(bool); isNAT {
			netID := asString(netState.Instance["docker_network_id"])
			if !connectedAtCreate[netID] {
				dockerSub, err := e.dockerSubstrate()
				if err != nil {
					_ = sub.DestroyNode(ctx, handle)
					return err
				}
				if err := dockerSub.ConnectContainerToNetwork(ctx, handle.ID, netID, link.IP); err != nil {
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

		nic, netNetns, err := e.wireLink(ctx, n.ID.Name, vethIdx, link, subName)
		if err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return err
		}
		nic.TargetName = fmt.Sprintf("eth%d", vethIdx)
		vethIdx++

		handleWithSrc := substrate.NodeHandle{
			ID: handle.ID,
			Attributes: mergeAttr(
				handle.Attributes,
				map[string]any{"network_netns": netNetns},
			),
		}
	// Firecracker needs bridge name for TAP attachment.
	if subName == "firecracker" && netState != nil {
		if brName := asString(netState.Instance["bridge"]); brName != "" {
			handleWithSrc.Attributes["network_bridge"] = brName
		}
	}
	if err := sub.AttachNIC(ctx, handleWithSrc, nic); err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return err
		}
		nics = append(nics, map[string]any{
			"kind":      nic.Kind, // "veth" or "tap"
			"host_end":  nic.HostEnd,
			"guest_end": nic.GuestEnd,
			"target":    nic.TargetName,
			"ip":        nic.IP,
			"netns":     netNetns,
		})
	}

	nodeInstance := map[string]any{
		"container_id": handle.ID,
		"nics":         nics,
	}
	// For firecracker, persist vsock metadata so post-apply tooling
	// (sensor, monitor, debugger) can reach the guest without rediscovery.
	if subName == "firecracker" {
		if uds, ok := handle.Attributes["vsock_uds"].(string); ok && uds != "" {
			nodeInstance["vsock_uds"] = uds
		}
		if cid, ok := handle.Attributes["vsock_cid"].(uint32); ok && cid != 0 {
			nodeInstance["vsock_cid"] = float64(cid)
		}
		if port, ok := handle.Attributes["vsock_port"].(uint32); ok && port != 0 {
			nodeInstance["vsock_port"] = float64(port)
		}
	}
	e.state.AddResource(state.Resource{
		Type:     "sysbox_node",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: nodeInstance,
	})

	// Start the node now that all NICs are attached.
	// For Docker this is a no-op (already started in CreateNode).
	// For Firecracker this launches the VM with the complete config.
	if err := sub.StartNode(ctx, handle); err != nil {
		_ = sub.DestroyNode(ctx, handle)
		return fmt.Errorf("start node %s: %w", n.ID.Name, err)
	}

	// For firecracker nodes, record the first link IP as ssh_ip so
	// provisioners can connect via SSH.
	if subName == "firecracker" && len(cfg.Links) > 0 {
		firstIP := cfg.Links[0].IP
		if firstIP != "" {
			// Strip CIDR suffix for SSH.
			if idx := strings.Index(firstIP, "/"); idx >= 0 {
				firstIP = firstIP[:idx]
			}
			if handle.Attributes == nil {
				handle.Attributes = map[string]any{}
			}
			handle.Attributes["ssh_ip"] = firstIP
			handle.Attributes["ssh_port"] = fmt.Sprintf("%d", cfg.SSHPort)
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
	handle := substrate.NodeHandle{ID: asString(r.Instance["container_id"])}
	// Ignore stop/destroy errors: container may already be gone (drift recovery).
	_ = sub.StopNode(ctx, handle)
	_ = sub.DestroyNode(ctx, handle)
	// Always clean up veths/taps and state regardless of container presence.
	if nics, ok := r.Instance["nics"].([]any); ok {
		for _, item := range nics {
			n, _ := item.(map[string]any)
			kind := asString(n["kind"])
			hostEnd := asString(n["host_end"])
			nsName := asString(n["netns"])
			if kind == "tap" {
				_ = network.DeleteTapDevice(hostEnd, nsName)
			} else {
				_ = network.DeleteVethPair(network.VethHandle{HostEnd: hostEnd, NetnsName: nsName})
			}
		}
	}
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// wireLink creates a veth pair (Docker) or tap device (Firecracker) in the
// network's netns and returns the NIC spec for the substrate to attach.
// Returns the network's netns name so the substrate knows where to find
// the guest-end / tap device.
// Caller must ensure the network is not a NAT network before calling wireLink.
func (e *Executor) wireLink(ctx context.Context, nodeName string, idx int, link config.LinkConfig, subName string) (substrate.NIC, string, error) {
	netName, err := resolveNetworkRef(link.Network)
	if err != nil {
		return substrate.NIC{}, "", err
	}
	netState := e.state.FindResource("sysbox_network", netName)
	if netState == nil {
		return substrate.NIC{}, "", fmt.Errorf("network %s not applied yet", netName)
	}

	nsName := asString(netState.Instance["netns"])
	brName := asString(netState.Instance["bridge"])

	// Firecracker uses TAP devices; Docker uses veth pairs.
	if subName == "firecracker" {
		tapName := fmt.Sprintf("tap-%05x-%d", fnvHash(nodeName)&0xfffff, idx)
		if len(tapName) > 15 {
			tapName = tapName[:15]
		}

		if err := network.CreateTapInNetns(tapName, nsName, brName); err != nil {
			return substrate.NIC{}, "", fmt.Errorf("create tap %s: %w", tapName, err)
		}

		return substrate.NIC{
			Kind:     "tap",
			HostEnd:  tapName,
			IP:       link.IP,
			Gateway:  link.Gateway,
		}, nsName, nil
	}

	// Default: Docker veth pair.
	hostEnd := vethName("vh", nodeName, idx)
	guestEnd := vethName("vg", nodeName, idx)

	// Only set a default gateway when explicitly requested by the caller.
	// Router interfaces intentionally omit gw so they don't compete for the
	// default route.
	gateway := link.Gateway

	pair, err := network.CreateVethPair(network.VethSpec{
		HostEnd:    hostEnd,
		GuestEnd:   guestEnd,
		NetnsName:  nsName,
		BridgeName: brName,
	})
	if err != nil {
		return substrate.NIC{}, "", err
	}

	return substrate.NIC{
		Kind:     "veth",
		HostEnd:  pair.HostEnd,
		GuestEnd: pair.GuestEnd,
		IP:       link.IP,
		Gateway:  gateway,
	}, nsName, nil
}

// fnvHash returns the FNV-1a 32-bit hash of s.
func fnvHash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// -- reference resolution helpers --
//
// After HCL EvalContext lands, references decode to bare strings:
//   substrate.docker.light    -> "docker"
//   sysbox_image.alpine.id    -> "alpine"
// We still accept legacy "type.name.attr" quoted strings for backwards
// compatibility with HCL files that don't use traversals.

func resolveSubstrateRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty substrate ref")
	}
	parts := strings.Split(ref, ".")
	switch len(parts) {
	case 1:
		return parts[0], nil
	case 3:
		return parts[1], nil
	default:
		return "", fmt.Errorf("unexpected substrate ref %q", ref)
	}
}

func resolveImageRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty image ref")
	}
	if !strings.Contains(ref, ".") {
		return ref, nil
	}
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("bad image ref %q", ref)
	}
	return parts[1], nil
}

// looksLikeKernelReference matches the same shape as the loader's
// looksLikeKernelRef but is duplicated here to keep pkg/runtime free of any
// dependency on cmd/sysbox/commands. Refs that look like literal paths or
// URLs are returned as-is by the caller (no state lookup).
func looksLikeKernelReference(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return false
	}
	if strings.Contains(s, "://") {
		return false
	}
	return true
}

// resolveKernelRef extracts the kernel resource name from either a bare name
// or a `sysbox_kernel.<name>.attr` traversal string.
func resolveKernelRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty kernel ref")
	}
	if !strings.Contains(ref, ".") {
		return ref, nil
	}
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("bad kernel ref %q", ref)
	}
	return parts[1], nil
}

func resolveNetworkRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty network ref")
	}
	if !strings.Contains(ref, ".") {
		return ref, nil
	}
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("bad network ref %q", ref)
	}
	return parts[1], nil
}

// vethName produces a deterministic ≤15-char interface name.
// Format: <prefix>-<5hexhash>-<idx>  e.g. "vh-a3f2c-0"
func vethName(prefix, nodeName string, idx int) string {
	h := fnv.New32a()
	h.Write([]byte(nodeName))
	return fmt.Sprintf("%s-%05x-%d", prefix, h.Sum32()&0xfffff, idx)
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// mergeAttr merges base and overlay attribute maps (overlay wins on conflict).
func mergeAttr(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

// -- sysbox_ssh_access --

func (e *Executor) createSSHAccess(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.SSHAccessConfig)
	if !ok {
		return fmt.Errorf("ssh_access %s: wrong data type", n.ID)
	}

	nodeName, err := resolveNodeRef(cfg.Node)
	if err != nil {
		return err
	}
	nodeState := e.state.FindResource("sysbox_node", nodeName)
	if nodeState == nil {
		return fmt.Errorf("node %s not applied yet", nodeName)
	}

	containerID := asString(nodeState.Instance["container_id"])
	handle := substrate.NodeHandle{
		ID: containerID,
		Attributes: map[string]any{"container_name": fmt.Sprintf("sysbox-%s", nodeName)},
	}

	// Find the docker substrate registered for the node.
	subName := nodeState.Provider
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}
	dockerSub, ok := sub.(*dockerprovider.Substrate)
	if !ok {
		return fmt.Errorf("sysbox_ssh_access requires a docker substrate, got %T", sub)
	}

	port := cfg.Port
	if port == 0 {
		port = 22
	}

	accessSpec := dockerprovider.SSHAccessSpec{
		NodeHandle:     handle,
		NodeID:         nodeName,
		AuthorizedKeys: cfg.AuthorizedKeys,
		Port:           port,
	}
	if err := dockerSub.SetupSSHAccess(ctx, accessSpec); err != nil {
		return fmt.Errorf("setup ssh access on %s: %w", nodeName, err)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_ssh_access",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"node": nodeName,
			"port": port,
		},
	})
	return nil
}

func resolveNodeRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty node ref")
	}
	if !strings.Contains(ref, ".") {
		return ref, nil
	}
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("bad node ref %q", ref)
	}
	return parts[1], nil
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
	uds, _ := handle.Attributes["vsock_uds"].(string)
	if uds == "" {
		return nil
	}
	port, _ := handle.Attributes["vsock_port"].(uint32)
	return providerexec.NewVsockConnection(uds, port)
}

// sshConnectionFromHandle builds an SSH connection from the node handle attributes.
func (e *Executor) sshConnectionFromHandle(handle substrate.NodeHandle, conns []config.ConnectionConfig) providerexec.Connection {
	host, _ := handle.Attributes["ssh_ip"].(string)
	port, _ := handle.Attributes["ssh_port"].(string)
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

// -- sysbox_agent --

func (e *Executor) createAgent(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.AgentConfig)
	if !ok {
		return fmt.Errorf("agent %s: wrong data type", n.ID)
	}

	nodeName, err := resolveNodeRef(cfg.Node)
	if err != nil {
		return err
	}
	nodeState := e.state.FindResource("sysbox_node", nodeName)
	if nodeState == nil {
		return fmt.Errorf("agent %s: node %s not applied yet", n.ID.Name, nodeName)
	}

	containerID := asString(nodeState.Instance["container_id"])
	subName := nodeState.Provider
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}
	dockerSub, ok := sub.(*dockerprovider.Substrate)
	if !ok {
		return fmt.Errorf("sysbox_agent requires a docker substrate, got %T", sub)
	}

	handle := substrate.NodeHandle{
		ID:         containerID,
		Attributes: map[string]any{"container_name": fmt.Sprintf("sysbox-%s", nodeName)},
	}

	fmt.Printf("[apply] starting agent %s on node %s: %v\n", n.ID.Name, nodeName, cfg.Command)
	pid, err := dockerSub.ExecBackground(ctx, handle, substrate.ExecSpec{
		Cmd: cfg.Command,
		Env: cfg.Env,
	})
	if err != nil {
		return fmt.Errorf("start agent %s: %w", n.ID.Name, err)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_agent",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"node":         nodeName,
			"container_id": containerID,
			"pid":          pid,
			"port":         cfg.Port,
			"command":      cfg.Command,
		},
	})
	fmt.Printf("[apply] agent %s started (container pid %d, port %d)\n", n.ID.Name, pid, cfg.Port)
	return nil
}

func (e *Executor) destroyAgent(ctx context.Context, r state.Resource) error {
	pid, _ := r.Instance["pid"].(float64) // JSON numbers decode as float64
	containerID := asString(r.Instance["container_id"])
	subName := r.Provider

	if pid > 0 && containerID != "" {
		sub, err := substrate.Get(subName)
		if err == nil {
			if dockerSub, ok := sub.(*dockerprovider.Substrate); ok {
				handle := substrate.NodeHandle{ID: containerID}
				// Kill the process by PID inside the container.
				killCmd := fmt.Sprintf("kill %d 2>/dev/null || true", int(pid))
				_, _ = dockerSub.ExecInNode(ctx, handle, substrate.ExecSpec{
					Cmd: []string{"sh", "-c", killCmd},
				})
			}
		}
	}

	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- sysbox_actor --

func (e *Executor) createActor(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.ActorConfig)
	if !ok {
		return fmt.Errorf("actor %s: wrong data type", n.ID)
	}
	position := cfg.Position
	if position == "" {
		position = "internal"
	}
	switch position {
	case "internal":
		return e.createInternalActor(ctx, n, cfg)
	case "external":
		return e.createExternalActor(ctx, n, cfg)
	default:
		return fmt.Errorf("actor %s: unknown position %q (must be internal or external)", n.ID.Name, position)
	}
}

// createInternalActor runs the actor command inside an existing sysbox_node.
// Semantics are identical to the legacy sysbox_agent but stored as sysbox_actor.
func (e *Executor) createInternalActor(ctx context.Context, n *graph.Node, cfg *config.ActorConfig) error {
	nodeName, err := resolveNodeRef(cfg.Node)
	if err != nil {
		return err
	}
	nodeState := e.state.FindResource("sysbox_node", nodeName)
	if nodeState == nil {
		return fmt.Errorf("actor %s: node %s not applied yet", n.ID.Name, nodeName)
	}

	containerID := asString(nodeState.Instance["container_id"])
	subName := nodeState.Provider
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}
	dockerSub, ok := sub.(*dockerprovider.Substrate)
	if !ok {
		return fmt.Errorf("sysbox_actor (internal) requires a docker substrate, got %T", sub)
	}

	handle := substrate.NodeHandle{
		ID:         containerID,
		Attributes: map[string]any{"container_name": fmt.Sprintf("sysbox-%s", nodeName)},
	}

	fmt.Printf("[apply] starting actor %s on node %s: %v\n", n.ID.Name, nodeName, cfg.Command)
	pid, err := dockerSub.ExecBackground(ctx, handle, substrate.ExecSpec{
		Cmd: cfg.Command,
		Env: cfg.Env,
	})
	if err != nil {
		return fmt.Errorf("start actor %s: %w", n.ID.Name, err)
	}

	// Determine ACP URL: use the node's IP on its Docker-managed network.
	acpURL := ""
	if ip, ipErr := dockerSub.GetContainerIP(ctx, containerID); ipErr == nil && cfg.Port > 0 {
		acpURL = fmt.Sprintf("http://%s:%d", ip, cfg.Port)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_actor",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"position":     "internal",
			"node":         nodeName,
			"container_id": containerID,
			"pid":          pid,
			"port":         cfg.Port,
			"acp_url":      acpURL,
			"entry_points": cfg.EntryPoints,
			"command":      cfg.Command,
		},
	})
	fmt.Printf("[apply] actor %s started (pid %d, acp %s)\n", n.ID.Name, pid, acpURL)
	return nil
}

// createExternalActor creates a standalone container outside the topology and
// runs the actor command in it. Only Docker bridge (NAT) network links are
// supported; the container gets no veth injection and no provisioners.
func (e *Executor) createExternalActor(ctx context.Context, n *graph.Node, cfg *config.ActorConfig) error {
	dockerSub, err := e.dockerSubstrate()
	if err != nil {
		return fmt.Errorf("actor %s: %w", n.ID.Name, err)
	}

	// Resolve image.
	imageName, err := resolveImageRef(cfg.Image)
	if err != nil {
		return err
	}
	imgState := e.state.FindResource("sysbox_image", imageName)
	if imgState == nil {
		return fmt.Errorf("actor %s: image %s not applied yet", n.ID.Name, imageName)
	}
	imgRef := substrate.ImageRef{
		ID:         asString(imgState.Instance["image_id"]),
		Repository: asString(imgState.Instance["repository"]),
	}

	// Collect Docker bridge (NAT) network links.
	type natLink struct{ netID, ip string }
	var natLinks []natLink
	for _, link := range cfg.Links {
		netName, err := resolveNetworkRef(link.Network)
		if err != nil {
			return err
		}
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			return fmt.Errorf("actor %s: network %s not applied yet", n.ID.Name, netName)
		}
		if isNAT, _ := netState.Instance["nat"].(bool); !isNAT {
			return fmt.Errorf("actor %s (external): link %s is not a NAT network; external actors only support Docker bridge networks", n.ID.Name, netName)
		}
		natLinks = append(natLinks, natLink{
			netID: asString(netState.Instance["docker_network_id"]),
			ip:    link.IP,
		})
	}

	// Build initial network attachment (first link at create time).
	var initialNets []substrate.DockerNetworkAttachment
	for _, nl := range natLinks {
		initialNets = append(initialNets, substrate.DockerNetworkAttachment{
			NetworkID: nl.netID,
			IPv4:      nl.ip,
		})
	}

	containerName := fmt.Sprintf("sysbox-actor-%s", n.ID.Name)
	handle, err := dockerSub.CreateNode(ctx, substrate.NodeSpec{
		Name:              containerName,
		Image:             imgRef,
		Env:               cfg.Env,
		InitialDockerNets: initialNets,
	})
	if err != nil {
		return fmt.Errorf("create actor container %s: %w", n.ID.Name, err)
	}

	if err := dockerSub.StartNode(ctx, handle); err != nil {
		_ = dockerSub.DestroyNode(ctx, handle)
		return fmt.Errorf("start actor container %s: %w", n.ID.Name, err)
	}

	// Connect remaining NAT networks (all after the first).
	for _, nl := range natLinks[min(1, len(natLinks)):] {
		if err := dockerSub.ConnectContainerToNetwork(ctx, handle.ID, nl.netID, nl.ip); err != nil {
			_ = dockerSub.DestroyNode(ctx, handle)
			return fmt.Errorf("actor %s: connect to network: %w", n.ID.Name, err)
		}
	}

	// Start the actor command inside the container.
	fmt.Printf("[apply] starting actor %s (external, %s): %v\n", n.ID.Name, containerName, cfg.Command)
	pid, err := dockerSub.ExecBackground(ctx, handle, substrate.ExecSpec{
		Cmd: cfg.Command,
		Env: cfg.Env,
	})
	if err != nil {
		_ = dockerSub.DestroyNode(ctx, handle)
		return fmt.Errorf("start actor command %s: %w", n.ID.Name, err)
	}

	acpURL := ""
	if ip, ipErr := dockerSub.GetContainerIP(ctx, handle.ID); ipErr == nil && cfg.Port > 0 {
		acpURL = fmt.Sprintf("http://%s:%d", ip, cfg.Port)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_actor",
		Name:     n.ID.Name,
		Provider: "docker",
		Instance: map[string]any{
			"position":       "external",
			"container_id":   handle.ID,
			"container_name": containerName,
			"pid":            pid,
			"port":           cfg.Port,
			"acp_url":        acpURL,
			"entry_points":   cfg.EntryPoints,
			"command":        cfg.Command,
		},
	})
	fmt.Printf("[apply] actor %s started (pid %d, acp %s)\n", n.ID.Name, pid, acpURL)
	return nil
}

func (e *Executor) destroyActor(ctx context.Context, r state.Resource) error {
	position, _ := r.Instance["position"].(string)
	pid, _ := r.Instance["pid"].(float64)
	containerID := asString(r.Instance["container_id"])

	sub, err := substrate.Get(r.Provider)
	if err != nil {
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	}
	dockerSub, ok := sub.(*dockerprovider.Substrate)
	if !ok {
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	}

	if pid > 0 && containerID != "" {
		handle := substrate.NodeHandle{ID: containerID}
		killCmd := fmt.Sprintf("kill %d 2>/dev/null || true", int(pid))
		_, _ = dockerSub.ExecInNode(ctx, handle, substrate.ExecSpec{
			Cmd: []string{"sh", "-c", killCmd},
		})
	}

	// External actors own their container; destroy it entirely.
	if position == "external" && containerID != "" {
		handle := substrate.NodeHandle{ID: containerID}
		_ = dockerSub.StopNode(ctx, handle)
		_ = dockerSub.DestroyNode(ctx, handle)
	}

	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- sysbox_monitor --

// createMonitor records the monitor intent in state. No EDR agent is deployed
// here; activation happens when `sysbox sensor start` reads the state and
// calls the registered MonitorBackend.Start().
//
// Separating declaration (Apply) from activation (sensor start) lets the same
// HCL field describe the monitoring topology without coupling the lifecycle to
// the apply graph — backends can be hot-restarted between episodes without
// re-applying the entire lab.
func (e *Executor) createMonitor(_ context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.MonitorConfig)
	if !ok {
		return fmt.Errorf("monitor %s: wrong data type", n.ID)
	}

	backend := cfg.Backend
	if backend == "" {
		backend = "tracee"
	}

	// Validate all referenced nodes exist at apply time.
	var nodeNames []string
	for _, nodeRef := range cfg.Nodes {
		nodeName := resolveRef(nodeRef)
		if nodeName == "" {
			return fmt.Errorf("monitor %s: cannot resolve node ref %q", n.ID.Name, nodeRef)
		}
		if e.state.FindResource("sysbox_node", nodeName) == nil {
			return fmt.Errorf("monitor %s: node %s not applied yet", n.ID.Name, nodeName)
		}
		nodeNames = append(nodeNames, nodeName)
	}

	// Store intent only: node names + backend config.
	// Runtime handles (container_id, mntns) are resolved dynamically at
	// sensor start so they always reflect the current node state, even
	// after a node is reprovisioned with a new container ID.
	e.state.AddResource(state.Resource{
		Type:     "sysbox_monitor",
		Name:     n.ID.Name,
		Provider: "monitor",
		Instance: map[string]any{
			"backend": backend,
			"nodes":   nodeNames,
			"events":  cfg.Events,
			"extra":   cfg.Extra,
		},
	})
	fmt.Printf("[apply] monitor %s  backend=%s  nodes=%v\n", n.ID.Name, backend, nodeNames)
	return nil
}

func (e *Executor) destroyMonitor(r state.Resource) error {
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// expandTilde replaces a leading ~ with the current user's home directory.
func expandTilde(path string) string {
	if len(path) == 0 || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + path[1:]
}

// dockerSubstrate returns the registered Docker substrate for use in
// network operations that need the Docker API directly.
func (e *Executor) dockerSubstrate() (*dockerprovider.Substrate, error) {
	sub, err := substrate.Get("docker")
	if err != nil {
		return nil, err
	}
	dockerSub, ok := sub.(*dockerprovider.Substrate)
	if !ok {
		return nil, fmt.Errorf("expected *docker.Substrate, got %T", sub)
	}
	return dockerSub, nil
}
