package runtime

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/oslab/sysbox/pkg/config"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
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
	case "sysbox_node":
		return e.createNode(ctx, node)
	case "sysbox_router":
		return e.createRouter(ctx, node)
	case "sysbox_firewall":
		return e.createFirewall(ctx, node)
	case "sysbox_ssh_access":
		return e.createSSHAccess(ctx, node)
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
	case "sysbox_firewall":
		return e.destroyFirewall(ctx, r)
	case "sysbox_ssh_access":
		e.state.RemoveResource(r.Type, r.Name)
		return nil
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

func (e *Executor) destroyNetwork(ctx context.Context, r state.Resource) error {
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

	ref, err := sub.PrepareImage(ctx, substrate.ImageSpec{
		DockerRef: cfg.DockerRef,
		Rootfs:    cfg.Rootfs,
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

	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:  fmt.Sprintf("sysbox-%s", n.ID.Name),
		Image: imgRef,
		Env:   cfg.Env,
	})
	if err != nil {
		return err
	}

	if err := sub.StartNode(ctx, handle); err != nil {
		_ = sub.DestroyNode(ctx, handle)
		return err
	}

	nics := []map[string]any{}
	for i, link := range cfg.Links {
		nic, netNetns, err := e.wireLink(ctx, n.ID.Name, i, link)
		if err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return err
		}
		nic.TargetName = fmt.Sprintf("eth%d", i)

		handleWithSrc := substrate.NodeHandle{
			ID: handle.ID,
			Attributes: map[string]any{
				"network_netns": netNetns,
			},
		}
		if err := sub.AttachNIC(ctx, handleWithSrc, nic); err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return err
		}
		nics = append(nics, map[string]any{
			"host_end":   nic.HostEnd,
			"guest_end":  nic.GuestEnd,
			"target":     nic.TargetName,
			"ip":         nic.IP,
			"netns":      netNetns,
		})
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_node",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"container_id": handle.ID,
			"nics":         nics,
		},
	})
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
	// Always clean up veths and state regardless of container presence.
	if nics, ok := r.Instance["nics"].([]any); ok {
		for _, item := range nics {
			n, _ := item.(map[string]any)
			hostEnd := asString(n["host_end"])
			nsName := asString(n["netns"])
			_ = network.DeleteVethPair(network.VethHandle{HostEnd: hostEnd, NetnsName: nsName})
		}
	}
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// wireLink creates a veth pair in the network's netns and returns the NIC
// spec for the substrate to attach. Returns the network's netns name so
// the substrate knows where to find the guest-end.
func (e *Executor) wireLink(ctx context.Context, nodeName string, idx int, link config.LinkConfig) (substrate.NIC, string, error) {
	netName, err := resolveNetworkRef(link.Network)
	if err != nil {
		return substrate.NIC{}, "", err
	}
	netState := e.state.FindResource("sysbox_network", netName)
	if netState == nil {
		return substrate.NIC{}, "", fmt.Errorf("network %s not applied yet", netName)
	}

	// Use a 5-char fnv32 hex hash so the name always fits in 15 chars
	// regardless of nodeName length: "vh-" + 5 hex + "-" + 1 digit = 10 chars.
	hostEnd := vethName("vh", nodeName, idx)
	guestEnd := vethName("vg", nodeName, idx)

	// Only set a default gateway when explicitly requested by the caller.
	// Router interfaces intentionally omit gw so they don't compete for the
	// default route.
	gateway := link.Gateway

	nsName := asString(netState.Instance["netns"])
	brName := asString(netState.Instance["bridge"])

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
