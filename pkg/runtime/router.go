package runtime

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// createRouter provisions a multi-NIC node with IP forwarding enabled.
// Interfaces on NAT (Docker-managed) networks are connected via Docker
// networking; isolated-network interfaces use veth pairs as usual.
// Optional NAT (nat_from -> nat_to) is delegated to the router network driver.
type RouterResourceHandler struct{}

func init() {
	RegisterResourceHandler(RouterResourceHandler{})
}

func (RouterResourceHandler) Type() string { return "sysbox_router" }

func (RouterResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_router")
}

func (RouterResourceHandler) Read(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	return readNodeLikeResource(ctx, current)
}

func (RouterResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (RouterResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.createRouterResource(ctx, n)
}

func (RouterResourceHandler) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	return pc.destroyNodeResource(ctx, current)
}

func (RouterResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}
func (RouterResourceHandler) RequiredCapabilities(node *graph.Node) ([]CapabilityRequirement, error) {
	cfg, ok := node.Data.(*config.RouterConfig)
	if !ok {
		return nil, nil
	}
	name, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return nil, err
	}
	required := []CapabilityRequirement{{name, driver.CapabilityNode}, {name, driver.CapabilityNIC}, {name, driver.CapabilityNodeState}}
	if cfg.NatFrom != "" || cfg.NatTo != "" {
		required = append(required, CapabilityRequirement{name, driver.CapabilityRouterNetwork})
	}
	return required, nil
}

func (RouterResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.RouterConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	if cfg.Image != "" {
		ref, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, ref)
	}
	for _, iface := range cfg.Interfaces {
		if iface.Network != "" {
			ref, err := config.ResolveResourceAddress(iface.Network, "sysbox_network")
			if err != nil {
				return nil, nil, err
			}
			deps = append(deps, ref)
		}
	}
	return cfg, deps, nil
}

func (e *Executor) createRouterResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.RouterConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("router %s: wrong data type", n.Address)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, err
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(subName)
	if err != nil {
		return state.Resource{}, err
	}
	nicDriver, err := driver.DefaultRegistry.RequireNIC(subName)
	if err != nil {
		return state.Resource{}, err
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(subName)
	if err != nil {
		return state.Resource{}, err
	}

	imageAddr, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
	if err != nil {
		return state.Resource{}, err
	}
	imgState := e.state.FindResource(imageAddr)
	if imgState == nil {
		return state.Resource{}, fmt.Errorf("image %s not applied yet", imageAddr)
	}
	imgRef := substrate.ImageRef{
		ID:         imgState.ImageID(),
		Repository: imgState.Repository(),
	}

	// Map RouterInterface → NICSpec for the shared wiring loop.
	var nicSpecs []NICSpec
	for _, iface := range cfg.Interfaces {
		nicSpecs = append(nicSpecs, NICSpec{
			Network: iface.Network,
			IP:      iface.IP,
			Label:   iface.Name,
		})
	}

	// Pre-scan: find the first NAT network for InitialLinks.
	initialLinks, err := collectNATLinks(e.state, nicSpecs, false)
	if err != nil {
		return state.Resource{}, err
	}

	handle, err := nodeDriver.CreateNode(ctx, substrate.NodeSpec{
		Name:         runtimeExternalName(e.topology, "router", n.Address.Name),
		Image:        imgRef,
		Sysctls:      map[string]string{"net.ipv4.ip_forward": "1"},
		InitialLinks: initialLinks,
		Labels:       ManagedLabels(e.topology, e.runID, n.Address),
	})
	if err != nil {
		return state.Resource{}, err
	}

	if err := nodeDriver.StartNode(ctx, handle); err != nil {
		util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy router on start failure")
		return state.Resource{}, err
	}

	// Wire all NICs using the shared helper (trackLabels=true for routers).
	wireResult, err := wireNICs(ctx, nicDriver, e.state, handle, initialLinks, nicSpecs, true, n.Address.Name)
	if err != nil {
		util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy router on wire failure")
		return state.Resource{}, err
	}

	natApplied := false
	if cfg.NatFrom != "" && cfg.NatTo != "" {
		fromIf, ok1 := wireResult.IfaceByName[cfg.NatFrom]
		toIf, ok2 := wireResult.IfaceByName[cfg.NatTo]
		if !ok1 || !ok2 {
			return state.Resource{}, fmt.Errorf("nat_from %q / nat_to %q must reference declared interfaces",
				cfg.NatFrom, cfg.NatTo)
		}
		routerNetwork, err := driver.DefaultRegistry.RequireRouterNetwork(subName)
		if err != nil {
			return state.Resource{}, err
		}
		if err := routerNetwork.ConfigureNAT(ctx, handle, fromIf, toIf); err != nil {
			e.logf("[router %s] warning: NAT setup failed (continuing without NAT): %v\n", n.Address.Name, err)
		} else {
			natApplied = true
		}
	}

	inst := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   wireResult.PrimaryIP,
		"nics":         wireResult.NICs,
		"nat_applied":  natApplied,
	}
	// Persist opaque provider state so cold-destroy works for all substrates.
	blob, _ := stateDriver.MarshalProviderState(handle)
	// Persist lifecycle flags.
	if lc := cfg.Lifecycle; lc != nil {
		inst["lifecycle_prevent_destroy"] = lc.PreventDestroy
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	resource := state.Resource{
		Address:    n.Address,
		Driver:     subName,
		Attributes: state.MustAttributes(inst),
	}
	if len(blob) > 0 {
		_ = resource.SetProviderState(blob)
	}
	return resource, nil
}
