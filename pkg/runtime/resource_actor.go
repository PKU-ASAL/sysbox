package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// -- sysbox_actor --

type ActorResourceProvider struct{}

func init() {
	RegisterResourceProvider(ActorResourceProvider{})
}

func (ActorResourceProvider) Type() string { return "sysbox_actor" }

func (ActorResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_actor")
}

func (ActorResourceProvider) Read(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	if current.Str("position") == "external" || current.ContainerID() != "" {
		return readNodeLikeResource(ctx, current)
	}
	return resourceReadOK(current), nil
}

func (ActorResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error) {
	return planDiffByDesiredHash(desired, current)
}

func (ActorResourceProvider) Create(ctx context.Context, exec *Executor, n *graph.Node) (state.Resource, error) {
	return exec.createActorResource(ctx, n)
}

func (p ActorResourceProvider) Update(ctx context.Context, exec *Executor, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, exec, desired)
}

func (ActorResourceProvider) Delete(ctx context.Context, exec *Executor, current state.Resource) error {
	return exec.destroyActorResource(ctx, current)
}

func (e *Executor) createActorResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.ActorConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("actor %s: wrong data type", n.ID)
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
		return state.Resource{}, fmt.Errorf("actor %s: unknown position %q (must be internal or external)", n.ID.Name, position)
	}
}

// createInternalActor runs the actor command inside an existing sysbox_node.
func (e *Executor) createInternalActor(ctx context.Context, n *graph.Node, cfg *config.ActorConfig) (state.Resource, error) {
	nodeName := config.ResolveName(cfg.Node)
	nodeState := e.state.FindResource("sysbox_node", nodeName)
	if nodeState == nil {
		return state.Resource{}, fmt.Errorf("actor %s: node %s not applied yet", n.ID.Name, nodeName)
	}

	subName := nodeState.Provider
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, err
	}

	// Reconstruct the handle from the persisted provider state so the
	// connection works on any substrate (docker, firecracker, libvirt).
	handle, err := nodeState.ReconstructHandle(sub)
	if err != nil {
		return state.Resource{}, fmt.Errorf("actor %s: %w", n.ID.Name, err)
	}
	e.logf("[apply] starting actor %s on node %s: %v\n", n.ID.Name, nodeName, cfg.Command)
	conn, err := sub.Connection(handle, nil)
	if err != nil {
		return state.Resource{}, fmt.Errorf("actor %s: connection: %w", n.ID.Name, err)
	}
	pid, err := conn.ExecBackground(ctx, cfg.Command, cfg.Env)
	if err != nil {
		return state.Resource{}, fmt.Errorf("start actor %s: %w", n.ID.Name, err)
	}

	// Determine ACP URL: prefer acp_ip if set, otherwise fall back to primary_ip.
	acpURL := ""
	if cfg.Port > 0 {
		ip := cfg.ACPIP
		if ip == "" {
			ip = nodeState.PrimaryIP()
		}
		if ip != "" {
			acpURL = fmt.Sprintf("http://%s:%d", ip, cfg.Port)
		}
	}

	inst := map[string]any{
		"position":     "internal",
		"node":         nodeName,
		"container_id": handle.ID,
		"pid":          pid,
		"port":         cfg.Port,
		"acp_url":      acpURL,
		"entry_points": cfg.EntryPoints,
		"command":      cfg.Command,
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	res := state.Resource{
		Type:     "sysbox_actor",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: inst,
	}
	e.logf("[apply] actor %s started (pid %d, acp %s)\n", n.ID.Name, pid, acpURL)
	return res, nil
}

// createExternalActor creates a standalone container outside the topology and
// runs the actor command in it. Only Docker bridge (NAT) network links are
// supported; the container gets no veth injection and no provisioners.
func (e *Executor) createExternalActor(ctx context.Context, n *graph.Node, cfg *config.ActorConfig) (state.Resource, error) {
	sub, err := substrate.Get("docker")
	if err != nil {
		return state.Resource{}, fmt.Errorf("actor %s: %w", n.ID.Name, err)
	}

	// Resolve image.
	imageName := config.ResolveName(cfg.Image)
	imgState := e.state.FindResource("sysbox_image", imageName)
	if imgState == nil {
		return state.Resource{}, fmt.Errorf("actor %s: image %s not applied yet", n.ID.Name, imageName)
	}
	imgRef := substrate.ImageRef{
		ID:         imgState.ImageID(),
		Repository: imgState.Repository(),
	}

	// Collect Docker bridge (NAT) network links.
	type natLink struct{ netID, ip string }
	var natLinks []natLink
	for _, link := range cfg.Links {
		netName := config.ResolveName(link.Network)
		netState := e.state.FindResource("sysbox_network", netName)
		if netState == nil {
			return state.Resource{}, fmt.Errorf("actor %s: network %s not applied yet", n.ID.Name, netName)
		}
		if !netState.IsNAT() {
			return state.Resource{}, fmt.Errorf("actor %s (external): link %s is not a NAT network; external actors only support Docker bridge networks", n.ID.Name, netName)
		}
		natLinks = append(natLinks, natLink{
			netID: netState.DockerNetID(),
			ip:    link.IP,
		})
	}

	// Build InitialLinks (first NAT network attached at create time).
	var initialLinks []substrate.LinkRequest
	for _, nl := range natLinks {
		initialLinks = append(initialLinks, substrate.LinkRequest{
			KindHint:    substrate.NICKindDockerNAT,
			DockerNetID: nl.netID,
			IP:          nl.ip,
		})
	}

	containerName := fmt.Sprintf("sysbox-actor-%s", n.ID.Name)
	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:         containerName,
		Image:        imgRef,
		Env:          cfg.Env,
		InitialLinks: initialLinks,
		Labels:       ManagedLabels(e.topology, e.runID, n.ID),
	})
	if err != nil {
		return state.Resource{}, fmt.Errorf("create actor container %s: %w", n.ID.Name, err)
	}

	if err := sub.StartNode(ctx, handle); err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy actor on start failure")
		return state.Resource{}, fmt.Errorf("start actor container %s: %w", n.ID.Name, err)
	}

	// Connect remaining NAT networks (all after the first) via AttachNIC.
	for _, nl := range natLinks[min(1, len(natLinks)):] {
		if _, err := sub.AttachNIC(ctx, handle, substrate.LinkRequest{
			KindHint:    substrate.NICKindDockerNAT,
			DockerNetID: nl.netID,
			IP:          nl.ip,
		}); err != nil {
			util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy actor on attach failure")
			return state.Resource{}, fmt.Errorf("actor %s: connect to network: %w", n.ID.Name, err)
		}
	}

	// Start the actor command inside the container.
	e.logf("[apply] starting actor %s (external, %s): %v\n", n.ID.Name, containerName, cfg.Command)
	conn, err := sub.Connection(handle, nil)
	if err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy actor on connection failure")
		return state.Resource{}, fmt.Errorf("actor %s: connection: %w", n.ID.Name, err)
	}
	pid, err := conn.ExecBackground(ctx, cfg.Command, cfg.Env)
	if err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy actor on exec failure")
		return state.Resource{}, fmt.Errorf("start actor command %s: %w", n.ID.Name, err)
	}

	acpURL := ""
	if cfg.Port > 0 {
		ip := cfg.ACPIP
		if ip == "" && len(natLinks) > 0 {
			ip = natLinks[0].ip
			if idx := strings.Index(ip, "/"); idx >= 0 {
				ip = ip[:idx]
			}
		}
		if ip != "" {
			acpURL = fmt.Sprintf("http://%s:%d", ip, cfg.Port)
		}
	}

	inst := map[string]any{
		"position":       "external",
		"container_id":   handle.ID,
		"container_name": containerName,
		"pid":            pid,
		"port":           cfg.Port,
		"acp_url":        acpURL,
		"entry_points":   cfg.EntryPoints,
		"command":        cfg.Command,
	}
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		inst["provider_extra"] = string(blob)
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	res := state.Resource{
		Type:     "sysbox_actor",
		Name:     n.ID.Name,
		Provider: "docker",
		Instance: inst,
	}
	e.logf("[apply] actor %s started (pid %d, acp %s)\n", n.ID.Name, pid, acpURL)
	return res, nil
}

func (e *Executor) destroyActorResource(ctx context.Context, r state.Resource) error {
	position := r.Str("position")
	pid := r.Int("pid")
	containerID := r.Str("container_id")

	sub, err := substrate.Get(r.Provider)
	if err != nil {
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	}

	if pid > 0 && containerID != "" {
		handle, err := r.ReconstructHandle(sub)
		if err != nil {
			e.logf("[destroy] warning: reconstruct actor %s: %v\n", r.Name, err)
			handle = substrate.NodeHandle{ID: containerID}
		}
		// Kill the entire process group so child processes are also
		// terminated (e.g. opencode-serve spawns sub-processes).
		killCmd := fmt.Sprintf("kill -- -%d 2>/dev/null; kill %d 2>/dev/null || true", pid, pid)
		if conn, err := sub.Connection(handle, nil); err == nil && conn != nil {
			util.BestEffortIgnore(func() error { return conn.ExecInline(ctx, []string{killCmd}) }, "kill actor process")
		}
	}

	// External actors own their container; destroy it entirely.
	if position == "external" && containerID != "" {
		handle, err := r.ReconstructHandle(sub)
		if err != nil {
			e.logf("[destroy] warning: reconstruct actor %s: %v\n", r.Name, err)
			handle = substrate.NodeHandle{ID: containerID}
		}
		if err := sub.StopNode(ctx, handle); err != nil {
			e.logf("[destroy] warning: stop actor %s: %v\n", r.Name, err)
		}
		if err := sub.DestroyNode(ctx, handle); err != nil {
			e.logf("[destroy] warning: destroy actor %s: %v\n", r.Name, err)
		}
	}

	e.state.RemoveResource(r.Type, r.Name)
	return nil
}
