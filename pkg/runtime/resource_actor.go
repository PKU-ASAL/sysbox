package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// -- sysbox_actor --

type ActorResourceHandler struct{}

func init() {
	RegisterResourceHandler(ActorResourceHandler{})
}

func (ActorResourceHandler) Type() string { return "sysbox_actor" }

func (ActorResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_actor")
}

func (ActorResourceHandler) Read(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	if current.Str("position") == "external" || current.ContainerID() != "" {
		return readNodeLikeResource(ctx, current)
	}
	return resourceReadOK(current), nil
}

func (ActorResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (ActorResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.createActorResource(ctx, n)
}

func (ActorResourceHandler) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	return pc.destroyActorResource(ctx, current)
}

func (ActorResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (ActorResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.ActorConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	position := cfg.Position
	if position == "" {
		position = "internal"
	}
	if position == "internal" {
		if cfg.Node != "" {
			ref, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
			if err != nil {
				return nil, nil, err
			}
			deps = append(deps, ref)
		}
	} else {
		if cfg.Image != "" {
			ref, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
			if err != nil {
				return nil, nil, err
			}
			deps = append(deps, ref)
		}
		for _, link := range cfg.Links {
			if link.Network != "" {
				ref, err := config.ResolveResourceAddress(link.Network, "sysbox_network")
				if err != nil {
					return nil, nil, err
				}
				deps = append(deps, ref)
			}
		}
	}
	deps, err := decodeDependsOn(deps, cfg.DependsOn)
	return cfg, deps, err
}

func (e *Executor) createActorResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.ActorConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("actor %s: wrong data type", n.Address)
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
		return state.Resource{}, fmt.Errorf("actor %s: unknown position %q (must be internal or external)", n.Address.Name, position)
	}
}

// createInternalActor runs the actor command inside an existing sysbox_node.
func (e *Executor) createInternalActor(ctx context.Context, n *graph.Node, cfg *config.ActorConfig) (state.Resource, error) {
	nodeAddr, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
	if err != nil {
		return state.Resource{}, err
	}
	nodeState := e.state.FindResource(nodeAddr)
	if nodeState == nil {
		return state.Resource{}, fmt.Errorf("actor %s: node %s not applied yet", n.Address.Name, nodeAddr)
	}

	subName := nodeState.Driver
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, err
	}

	// Reconstruct the handle from the persisted provider state so the
	// connection works on any substrate (docker, firecracker, libvirt).
	handle, err := nodeState.ReconstructHandle(sub)
	if err != nil {
		return state.Resource{}, fmt.Errorf("actor %s: %w", n.Address.Name, err)
	}
	e.logf("[apply] starting actor %s on node %s: %v\n", n.Address.Name, nodeAddr, cfg.Command)
	conn, err := sub.Connection(handle, nil)
	if err != nil {
		return state.Resource{}, fmt.Errorf("actor %s: connection: %w", n.Address.Name, err)
	}
	resolvedCommand, err := resolveSecretStrings(ctx, cfg.Command)
	if err != nil {
		return state.Resource{}, err
	}
	resolvedEnv, err := resolveSecretMap(ctx, cfg.Env)
	if err != nil {
		return state.Resource{}, err
	}
	pid, err := conn.ExecBackground(ctx, resolvedCommand, resolvedEnv)
	if err != nil {
		return state.Resource{}, fmt.Errorf("start actor %s: %w", n.Address.Name, err)
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
		"node":         nodeAddr.String(),
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
		Address:    n.Address,
		Driver:     subName,
		Attributes: state.MustAttributes(inst),
	}
	e.logf("[apply] actor %s started (pid %d, acp %s)\n", n.Address.Name, pid, acpURL)
	return res, nil
}

// createExternalActor creates a standalone container outside the topology and
// runs the actor command in it. Only Docker bridge (NAT) network links are
// supported; the container gets no veth injection and no provisioners.
func (e *Executor) createExternalActor(ctx context.Context, n *graph.Node, cfg *config.ActorConfig) (state.Resource, error) {
	sub, err := substrate.Get("docker")
	if err != nil {
		return state.Resource{}, fmt.Errorf("actor %s: %w", n.Address.Name, err)
	}

	// Resolve image.
	imageAddr, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
	if err != nil {
		return state.Resource{}, err
	}
	imgState := e.state.FindResource(imageAddr)
	if imgState == nil {
		return state.Resource{}, fmt.Errorf("actor %s: image %s not applied yet", n.Address.Name, imageAddr)
	}
	imgRef := substrate.ImageRef{
		ID:         imgState.ImageID(),
		Repository: imgState.Repository(),
	}

	// Collect Docker bridge (NAT) network links.
	type natLink struct{ netID, ip string }
	var natLinks []natLink
	for _, link := range cfg.Links {
		netAddr, err := config.ResolveResourceAddress(link.Network, "sysbox_network")
		if err != nil {
			return state.Resource{}, err
		}
		netState := e.state.FindResource(netAddr)
		if netState == nil {
			return state.Resource{}, fmt.Errorf("actor %s: network %s not applied yet", n.Address.Name, netAddr)
		}
		if !netState.IsNAT() {
			return state.Resource{}, fmt.Errorf("actor %s (external): link %s is not a NAT network; external actors only support Docker bridge networks", n.Address.Name, netAddr)
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

	containerName := runtimeExternalName(e.topology, "actor", n.Address.Name)
	resolvedEnv, err := resolveSecretMap(ctx, cfg.Env)
	if err != nil {
		return state.Resource{}, err
	}
	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:         containerName,
		Image:        imgRef,
		Env:          resolvedEnv,
		InitialLinks: initialLinks,
		Labels:       ManagedLabels(e.topology, e.runID, n.Address),
	})
	if err != nil {
		return state.Resource{}, fmt.Errorf("create actor container %s: %w", n.Address.Name, err)
	}

	if err := sub.StartNode(ctx, handle); err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy actor on start failure")
		return state.Resource{}, fmt.Errorf("start actor container %s: %w", n.Address.Name, err)
	}

	// Connect remaining NAT networks (all after the first) via AttachNIC.
	for _, nl := range natLinks[min(1, len(natLinks)):] {
		if _, err := sub.AttachNIC(ctx, handle, substrate.LinkRequest{
			KindHint:    substrate.NICKindDockerNAT,
			DockerNetID: nl.netID,
			IP:          nl.ip,
		}); err != nil {
			util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy actor on attach failure")
			return state.Resource{}, fmt.Errorf("actor %s: connect to network: %w", n.Address.Name, err)
		}
	}

	// Start the actor command inside the container.
	e.logf("[apply] starting actor %s (external, %s): %v\n", n.Address.Name, containerName, cfg.Command)
	conn, err := sub.Connection(handle, nil)
	if err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy actor on connection failure")
		return state.Resource{}, fmt.Errorf("actor %s: connection: %w", n.Address.Name, err)
	}
	resolvedCommand, err := resolveSecretStrings(ctx, cfg.Command)
	if err != nil {
		return state.Resource{}, err
	}
	pid, err := conn.ExecBackground(ctx, resolvedCommand, resolvedEnv)
	if err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy actor on exec failure")
		return state.Resource{}, fmt.Errorf("start actor command %s: %w", n.Address.Name, err)
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
	blob, _ := sub.MarshalProviderState(handle)
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	res := state.Resource{
		Address:    n.Address,
		Driver:     "docker",
		Attributes: state.MustAttributes(inst),
	}
	if len(blob) > 0 {
		_ = res.SetProviderState(blob)
	}
	e.logf("[apply] actor %s started (pid %d, acp %s)\n", n.Address.Name, pid, acpURL)
	return res, nil
}

func (e *Executor) destroyActorResource(ctx context.Context, r state.Resource) error {
	position := r.Str("position")
	pid := r.Int("pid")
	containerID := r.Str("container_id")

	sub, err := substrate.Get(r.Driver)
	if err != nil {
		e.state.RemoveResource(r.Address)
		return nil
	}

	if pid > 0 && containerID != "" {
		handle, err := r.ReconstructHandle(sub)
		if err != nil {
			e.logf("[destroy] warning: reconstruct actor %s: %v\n", r.Address.Name, err)
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
			e.logf("[destroy] warning: reconstruct actor %s: %v\n", r.Address.Name, err)
			handle = substrate.NodeHandle{ID: containerID}
		}
		if err := sub.StopNode(ctx, handle); err != nil {
			e.logf("[destroy] warning: stop actor %s: %v\n", r.Address.Name, err)
		}
		if err := sub.DestroyNode(ctx, handle); err != nil {
			e.logf("[destroy] warning: destroy actor %s: %v\n", r.Address.Name, err)
		}
	}

	e.state.RemoveResource(r.Address)
	return nil
}
