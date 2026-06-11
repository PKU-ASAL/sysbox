package runtime

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// readDataNode queries a substrate for an existing node and records it in
// state. Unlike createNode, this does not create any infrastructure — it
// merely reads the node's current attributes so other resources can reference
// them in the eval context.
type DataNodeResourceProvider struct{}

func init() {
	RegisterResourceProvider(DataNodeResourceProvider{})
	RegisterResourceProvider(DataNetworkResourceProvider{})
	RegisterResourceProvider(DataImageResourceProvider{})
}

func (DataNodeResourceProvider) Type() string { return "data_sysbox_node" }

func (DataNodeResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("data_sysbox_node")
}

func (DataNodeResourceProvider) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return resourceReadOK(current), nil
}

func (DataNodeResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	return planDiffForDataSource(desired, current)
}

func (DataNodeResourceProvider) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.readDataNodeResource(ctx, n)
}

func (p DataNodeResourceProvider) Update(ctx context.Context, pc *ProviderContext, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, pc, desired)
}

func (DataNodeResourceProvider) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	pc.State().RemoveResource(current.Type, current.Name)
	return nil
}

func (DataNodeResourceProvider) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (e *Executor) readDataNodeResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.DataNodeConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("data sysbox_node.%s: wrong data type", n.ID.Name)
	}

	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_node.%s: %w", n.ID.Name, err)
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_node.%s: %w", n.ID.Name, err)
	}

	handle, err := sub.ReadNode(ctx, cfg.ID)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_node.%s: read %q: %w", n.ID.Name, cfg.ID, err)
	}

	inst := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   handle.Net.PrimaryIP,
	}
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		inst["provider_extra"] = string(blob)
	}
	inst["data_read"] = true // mark as read-only so destroy skips it
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}

	res := state.Resource{
		Type:     "data_sysbox_node",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: inst,
	}
	e.logf("[data] read sysbox_node.%s → id=%s ip=%s\n", n.ID.Name, handle.ID, handle.Net.PrimaryIP)
	return res, nil
}

// readDataNetwork queries an existing Docker network by name and records
// its attributes in state. Unlike readDataNode, this is substrate-specific
// because sysbox_network is only managed by Docker currently.
type DataNetworkResourceProvider struct{}

func (DataNetworkResourceProvider) Type() string { return "data_sysbox_network" }

func (DataNetworkResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("data_sysbox_network")
}

func (DataNetworkResourceProvider) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return resourceReadOK(current), nil
}

func (DataNetworkResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	return planDiffForDataSource(desired, current)
}

func (DataNetworkResourceProvider) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.readDataNetworkResource(ctx, n)
}

func (p DataNetworkResourceProvider) Update(ctx context.Context, pc *ProviderContext, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, pc, desired)
}

func (DataNetworkResourceProvider) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	pc.State().RemoveResource(current.Type, current.Name)
	return nil
}

func (DataNetworkResourceProvider) ExternalID(current state.Resource) string {
	if id := current.DockerNetID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (DataNetworkResourceProvider) DecodeData(d config.DataBlock, ctx *hcl.EvalContext) (any, []graph.Ref, error) {
	cfg := &config.DataNetworkConfig{}
	if err := decodeDataBody(d.Remain, ctx, cfg, "sysbox_network", d.Name); err != nil {
		return nil, nil, err
	}
	return cfg, nil, nil
}

func (e *Executor) readDataNetworkResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.DataNetworkConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("data sysbox_network.%s: wrong data type", n.ID.Name)
	}

	sub, err := substrate.Get("docker")
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_network.%s: requires docker substrate: %w", n.ID.Name, err)
	}

	// Try the user-given name directly first (e.g. "bridge" or a custom
	// network). If not found, try the sysbox-prefixed variant. This covers
	// both externally-managed Docker networks and sysbox-managed ones.
	info, err := sub.ReadManagedNetwork(ctx, substrate.ManagedNetworkSpec{Name: cfg.Name})
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_network.%s: network %q not found: %w", n.ID.Name, cfg.Name, err)
	}

	inst := map[string]any{
		"docker_network_id": info.ID,
		"docker_net_name":   info.Name,
		"data_read":         true,
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	res := state.Resource{
		Type:     "data_sysbox_network",
		Name:     n.ID.Name,
		Provider: "docker",
		Instance: inst,
	}
	e.logf("[data] read sysbox_network.%s → %s\n", n.ID.Name, info.Name)
	return res, nil
}

// readDataImage queries an existing Docker image and records its metadata.
type DataImageResourceProvider struct{}

func (DataImageResourceProvider) Type() string { return "data_sysbox_image" }

func (DataImageResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("data_sysbox_image")
}

func (DataImageResourceProvider) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return resourceReadOK(current), nil
}

func (DataImageResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	return planDiffForDataSource(desired, current)
}

func (DataImageResourceProvider) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.readDataImageResource(ctx, n)
}

func (p DataImageResourceProvider) Update(ctx context.Context, pc *ProviderContext, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, pc, desired)
}

func (DataImageResourceProvider) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	pc.State().RemoveResource(current.Type, current.Name)
	return nil
}

func (DataImageResourceProvider) ExternalID(current state.Resource) string {
	if id := current.ImageID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (e *Executor) readDataImageResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.DataImageConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: wrong data type", n.ID.Name)
	}

	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: %w", n.ID.Name, err)
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: %w", n.ID.Name, err)
	}

	if cfg.DockerRef == "" {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: docker_ref is required", n.ID.Name)
	}

	ref, err := sub.PrepareImage(ctx, substrate.ImageSpec{DockerRef: cfg.DockerRef})
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: %w", n.ID.Name, err)
	}

	inst := map[string]any{
		"image_id":  ref.ID,
		"repo":      ref.Repository,
		"data_read": true,
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	res := state.Resource{
		Type:     "data_sysbox_image",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: inst,
	}
	e.logf("[data] read sysbox_image.%s → %s\n", n.ID.Name, ref.ID)
	return res, nil
}
