package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

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
	nodeName := config.ResolveName(cfg.Node)
	nodeState := e.state.FindResource("sysbox_node", nodeName)
	if nodeState == nil {
		return fmt.Errorf("actor %s: node %s not applied yet", n.ID.Name, nodeName)
	}

	containerID := util.AsString(nodeState.Instance["container_id"])
	subName := nodeState.Provider
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}
	dockerCap, ok := sub.(substrate.DockerCapable)
	if !ok {
		return fmt.Errorf("sysbox_actor (internal) requires a DockerCapable substrate, got %T", sub)
	}

	handle := substrate.NodeHandle{
		ID:         containerID,
		Attributes: map[string]any{"container_name": fmt.Sprintf("sysbox-%s", nodeName)},
	}

	fmt.Printf("[apply] starting actor %s on node %s: %v\n", n.ID.Name, nodeName, cfg.Command)
	pid, err := dockerCap.ExecBackground(ctx, handle, substrate.ExecSpec{
		Cmd: cfg.Command,
		Env: cfg.Env,
	})
	if err != nil {
		return fmt.Errorf("start actor %s: %w", n.ID.Name, err)
	}

	// Determine ACP URL: use the node's IP on its Docker-managed network.
	acpURL := ""
	if ip, ipErr := dockerCap.GetContainerIP(ctx, containerID); ipErr == nil && cfg.Port > 0 {
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
	sub, err := substrate.Get("docker")
	if err != nil {
		return fmt.Errorf("actor %s: %w", n.ID.Name, err)
	}
	dockerCap, ok := sub.(substrate.DockerCapable)
	if !ok {
		return fmt.Errorf("actor %s: docker substrate does not implement DockerCapable", n.ID.Name)
	}

	// Resolve image.
	imageName := config.ResolveName(cfg.Image)
	imgState := e.state.FindResource("sysbox_image", imageName)
	if imgState == nil {
		return fmt.Errorf("actor %s: image %s not applied yet", n.ID.Name, imageName)
	}
	imgRef := substrate.ImageRef{
		ID:         util.AsString(imgState.Instance["image_id"]),
		Repository: util.AsString(imgState.Instance["repository"]),
	}

	// Collect Docker bridge (NAT) network links.
	type natLink struct{ netID, ip string }
	var natLinks []natLink
	for _, link := range cfg.Links {
		netName := config.ResolveName(link.Network)
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			return fmt.Errorf("actor %s: network %s not applied yet", n.ID.Name, netName)
		}
		if isNAT, _ := netState.Instance["nat"].(bool); !isNAT {
			return fmt.Errorf("actor %s (external): link %s is not a NAT network; external actors only support Docker bridge networks", n.ID.Name, netName)
		}
		natLinks = append(natLinks, natLink{
			netID: util.AsString(netState.Instance["docker_network_id"]),
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
	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:              containerName,
		Image:             imgRef,
		Env:               cfg.Env,
		InitialDockerNets: initialNets,
	})
	if err != nil {
		return fmt.Errorf("create actor container %s: %w", n.ID.Name, err)
	}

	if err := sub.StartNode(ctx, handle); err != nil {
		_ = sub.DestroyNode(ctx, handle)
		return fmt.Errorf("start actor container %s: %w", n.ID.Name, err)
	}

	// Connect remaining NAT networks (all after the first).
	for _, nl := range natLinks[min(1, len(natLinks)):] {
		if err := dockerCap.ConnectContainerToNetwork(ctx, handle.ID, nl.netID, nl.ip); err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return fmt.Errorf("actor %s: connect to network: %w", n.ID.Name, err)
		}
	}

	// Start the actor command inside the container.
	fmt.Printf("[apply] starting actor %s (external, %s): %v\n", n.ID.Name, containerName, cfg.Command)
	pid, err := dockerCap.ExecBackground(ctx, handle, substrate.ExecSpec{
		Cmd: cfg.Command,
		Env: cfg.Env,
	})
	if err != nil {
		_ = sub.DestroyNode(ctx, handle)
		return fmt.Errorf("start actor command %s: %w", n.ID.Name, err)
	}

	acpURL := ""
	if ip, ipErr := dockerCap.GetContainerIP(ctx, handle.ID); ipErr == nil && cfg.Port > 0 {
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
	containerID := util.AsString(r.Instance["container_id"])

	sub, err := substrate.Get(r.Provider)
	if err != nil {
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	}

	if pid > 0 && containerID != "" {
		handle := substrate.NodeHandle{ID: containerID}
		killCmd := fmt.Sprintf("kill %d 2>/dev/null || true", int(pid))
		_, _ = sub.ExecInNode(ctx, handle, substrate.ExecSpec{
			Cmd: []string{"sh", "-c", killCmd},
		})
	}

	// External actors own their container; destroy it entirely.
	if position == "external" && containerID != "" {
		handle := substrate.NodeHandle{ID: containerID}
		if err := sub.StopNode(ctx, handle); err != nil {
			fmt.Printf("[destroy] warning: stop actor %s: %v\n", r.Name, err)
		}
		if err := sub.DestroyNode(ctx, handle); err != nil {
			fmt.Printf("[destroy] warning: destroy actor %s: %v\n", r.Name, err)
		}
	}

	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- sysbox_ssh_access --

func (e *Executor) createSSHAccess(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.SSHAccessConfig)
	if !ok {
		return fmt.Errorf("ssh_access %s: wrong data type", n.ID)
	}

	nodeName := config.ResolveName(cfg.Node)
	nodeState := e.state.FindResource("sysbox_node", nodeName)
	if nodeState == nil {
		return fmt.Errorf("node %s not applied yet", nodeName)
	}

	containerID := util.AsString(nodeState.Instance["container_id"])
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

// -- sysbox_agent (legacy, maps to internal actor) --

func (e *Executor) createAgent(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.AgentConfig)
	if !ok {
		return fmt.Errorf("agent %s: wrong data type", n.ID)
	}

	// Map legacy sysbox_agent to sysbox_actor with position="internal".
	actorCfg := config.ActorConfig{
		Position: "internal",
		Node:     cfg.Node,
		Command:  cfg.Command,
		Port:     cfg.Port,
		Env:      cfg.Env,
		DependsOn: cfg.DependsOn,
	}
	return e.createInternalActor(ctx, n, &actorCfg)
}

func (e *Executor) destroyAgent(ctx context.Context, r state.Resource) error {
	return e.destroyActor(ctx, r)
}
