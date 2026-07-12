package runtime

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

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
type DataNodeResourceHandler struct{}

func init() {
	RegisterResourceHandler(DataNodeResourceHandler{})
	RegisterResourceHandler(DataNetworkResourceHandler{})
	RegisterResourceHandler(DataImageResourceHandler{})
}

func (DataNodeResourceHandler) Type() string { return "data_sysbox_node" }

func (DataNodeResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("data_sysbox_node")
}

func (DataNodeResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return resourceReadOK(current), nil
}

func (DataNodeResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffForDataSource(desired, current)
}

func (DataNodeResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.readDataNodeResource(ctx, n)
}

func (DataNodeResourceHandler) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	pc.State().RemoveResource(current.Address)
	return nil
}

func (DataNodeResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (e *Executor) readDataNodeResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.DataNodeConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("data sysbox_node.%s: wrong data type", n.Address.Name)
	}

	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_node.%s: %w", n.Address.Name, err)
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_node.%s: %w", n.Address.Name, err)
	}

	handle, err := sub.ReadNode(ctx, cfg.ID)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_node.%s: read %q: %w", n.Address.Name, cfg.ID, err)
	}

	inst := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   handle.Net.PrimaryIP,
	}
	blob, _ := sub.MarshalProviderState(handle)
	inst["data_read"] = true // mark as read-only so destroy skips it
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}

	res := state.Resource{
		Address:    n.Address,
		Driver:     subName,
		Attributes: state.MustAttributes(inst),
	}
	if len(blob) > 0 {
		_ = res.SetProviderState(blob)
	}
	e.logf("[data] read sysbox_node.%s → id=%s ip=%s\n", n.Address.Name, handle.ID, handle.Net.PrimaryIP)
	return res, nil
}

// readDataNetwork queries an existing Docker network by name and records
// its attributes in state. Unlike readDataNode, this is substrate-specific
// because sysbox_network is only managed by Docker currently.
type DataNetworkResourceHandler struct{}

func (DataNetworkResourceHandler) Type() string { return "data_sysbox_network" }

func (DataNetworkResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("data_sysbox_network")
}

func (DataNetworkResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return resourceReadOK(current), nil
}

func (DataNetworkResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffForDataSource(desired, current)
}

func (DataNetworkResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.readDataNetworkResource(ctx, n)
}

func (DataNetworkResourceHandler) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	pc.State().RemoveResource(current.Address)
	return nil
}

func (DataNetworkResourceHandler) ExternalID(current state.Resource) string {
	if id := current.DockerNetID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (DataNetworkResourceHandler) DecodeData(d config.DataBlock, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.DataNetworkConfig{}
	if err := decodeDataBody(d.Remain, ctx, cfg, "sysbox_network", d.Name); err != nil {
		return nil, nil, err
	}
	return cfg, nil, nil
}

func (e *Executor) readDataNetworkResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.DataNetworkConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("data sysbox_network.%s: wrong data type", n.Address.Name)
	}

	sub, err := substrate.Get("docker")
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_network.%s: requires docker substrate: %w", n.Address.Name, err)
	}

	// Try the user-given name directly first (e.g. "bridge" or a custom
	// network). If not found, try the sysbox-prefixed variant. This covers
	// both externally-managed Docker networks and sysbox-managed ones.
	info, err := sub.ReadManagedNetwork(ctx, substrate.ManagedNetworkSpec{Name: cfg.Name})
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_network.%s: network %q not found: %w", n.Address.Name, cfg.Name, err)
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
		Address:    n.Address,
		Driver:     "docker",
		Attributes: state.MustAttributes(inst),
	}
	e.logf("[data] read sysbox_network.%s → %s\n", n.Address.Name, info.Name)
	return res, nil
}

// readDataImage queries an existing Docker image and records its metadata.
type DataImageResourceHandler struct{}

func (DataImageResourceHandler) Type() string { return "data_sysbox_image" }

func (DataImageResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("data_sysbox_image")
}

func (DataImageResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return resourceReadOK(current), nil
}

func (DataImageResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffForDataSource(desired, current)
}

func (DataImageResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.readDataImageResource(ctx, n)
}

func (DataImageResourceHandler) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	pc.State().RemoveResource(current.Address)
	return nil
}

func (DataImageResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ImageID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (e *Executor) readDataImageResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.DataImageConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: wrong data type", n.Address.Name)
	}

	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: %w", n.Address.Name, err)
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: %w", n.Address.Name, err)
	}

	if cfg.DockerRef == "" {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: docker_ref is required", n.Address.Name)
	}

	ref, err := sub.PrepareImage(ctx, substrate.ImageSpec{DockerRef: cfg.DockerRef})
	if err != nil {
		return state.Resource{}, fmt.Errorf("data sysbox_image.%s: %w", n.Address.Name, err)
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
		Address:    n.Address,
		Driver:     subName,
		Attributes: state.MustAttributes(inst),
	}
	e.logf("[data] read sysbox_image.%s → %s\n", n.Address.Name, ref.ID)
	return res, nil
}
