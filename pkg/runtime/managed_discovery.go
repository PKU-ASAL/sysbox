package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (NetworkResourceHandler) Discover(ctx context.Context, pc *ProviderContext, n *graph.Node) (ResourceReadResult, error) {
	cfg, ok := n.Data.(*config.NetworkConfig)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("invalid network configuration")
	}
	if !cfg.NAT {
		return discoverIsolatedNetwork(ctx, pc, n, cfg)
	}
	networkDriver, err := pc.exec.providers.RequireNetwork("docker")
	if err != nil {
		return ResourceReadResult{}, err
	}
	info, err := networkDriver.ReadManagedNetwork(ctx, substrate.ManagedNetworkSpec{Name: networkExternalName(pc.Topology(), n.Address), CIDR: cfg.CIDR, NAT: true, ExposeBridge: requiresManagedBridge(pc.exec.graph, n.Address), Labels: ManagedLabels(pc.Topology(), "", n.Address)})
	if driver.IsCategory(err, driver.ErrorNotFound) {
		return ResourceReadResult{Status: state.ResourceAbsent}, nil
	}
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown}, err
	}
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("missing discovery ownership")
	}
	attrs := map[string]any{"nat": true, "docker_network_id": info.ID, "docker_net_name": info.Name, "cidr": cfg.CIDR}
	if err := setDesiredHash(n, attrs); err != nil {
		return ResourceReadResult{}, err
	}
	r := state.Resource{Address: n.Address, Driver: "docker", ExternalID: info.ID, Attributes: state.MustAttributes(attrs), OwnerUID: identity.OwnerUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation}
	if info.BridgeTarget != nil {
		if err := r.SetRuntimeValue("managed_bridge", info.BridgeTarget); err != nil {
			return ResourceReadResult{}, err
		}
	}
	return ResourceReadResult{Status: state.ResourcePresent, Resource: r}, nil
}

func discoverIsolatedNetwork(ctx context.Context, pc *ProviderContext, n *graph.Node, cfg *config.NetworkConfig) (ResourceReadResult, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("missing network discovery identity")
	}
	name := networkExternalName(pc.Topology(), n.Address)
	gateway, err := gatewayCIDR(cfg.CIDR)
	if err != nil {
		return ResourceReadResult{}, err
	}
	attrs := map[string]any{"netns": "sysbox-net-" + name, "bridge": shortLinuxName("br", name), "cidr": cfg.CIDR, "gateway": gateway, "libvirt_bridge": shortLinuxName("lv", name), "root_veth": shortLinuxName("lvr", name), "netns_veth": shortLinuxName("lvn", name), "root_address": cfg.RootAddress}
	if len(cfg.RootRoutes) > 0 {
		routes := make([]map[string]string, 0, len(cfg.RootRoutes))
		for _, r := range cfg.RootRoutes {
			routes = append(routes, map[string]string{"dst": r.Destination, "via": r.Via})
		}
		attrs["root_routes"] = routes
	}
	if cfg.Lifecycle != nil {
		attrs["lifecycle_prevent_destroy"] = cfg.Lifecycle.PreventDestroy
	}
	if err := setDesiredHash(n, attrs); err != nil {
		return ResourceReadResult{}, err
	}
	candidate := state.Resource{Address: n.Address, Driver: "network", ExternalID: "sysbox-net-" + name, Attributes: state.MustAttributes(attrs), OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, Generation: id.Generation}
	return (NetworkResourceHandler{}).Read(ctx, candidate, pc.exec.providers)
}

func (NodeResourceHandler) Discover(ctx context.Context, pc *ProviderContext, n *graph.Node) (ResourceReadResult, error) {
	cfg, ok := n.Data.(*config.NodeConfig)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("invalid node configuration")
	}
	return discoverNode(ctx, pc, n, cfg.Substrate, "node")
}

func (RouterResourceHandler) Discover(ctx context.Context, pc *ProviderContext, n *graph.Node) (ResourceReadResult, error) {
	cfg, ok := n.Data.(*config.RouterConfig)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("invalid router configuration")
	}
	return discoverNode(ctx, pc, n, cfg.Substrate, "router")
}

func discoverNode(ctx context.Context, pc *ProviderContext, n *graph.Node, substrateRef, kind string) (ResourceReadResult, error) {
	return discoverNodeIdentity(ctx, pc, n, substrateRef, kind, false)
}

// incomplete is reserved for explicit intent disposition, never ordinary recovery.
func discoverNodeIdentity(ctx context.Context, pc *ProviderContext, n *graph.Node, substrateRef, kind string, incomplete bool) (ResourceReadResult, error) {
	provider, err := resolveSubstrateRef(substrateRef)
	if err != nil {
		return ResourceReadResult{}, err
	}
	nodeDriver, err := pc.exec.providers.RequireNode(provider)
	if err != nil {
		return ResourceReadResult{}, err
	}
	discovery, ok := nodeDriver.(driver.ManagedDiscovery)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("%w: driver %s has no managed node discovery", ErrConvergenceUnsupported, provider)
	}
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("%w: discovery has no persisted owner", ErrObservationUnknown)
	}
	handle, err := discovery.DiscoverManagedNode(ctx, identity, runtimeExternalName(pc.Topology(), kind, n.Address))
	if driver.IsCategory(err, driver.ErrorNotFound) {
		return ResourceReadResult{Status: state.ResourceAbsent}, nil
	}
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown}, err
	}
	stateDriver, err := pc.exec.providers.RequireNodeState(provider)
	if err != nil {
		return ResourceReadResult{}, err
	}
	attrs := substrate.HandlePublicAttributes(handle)
	attrs["container_id"] = handle.ID
	// Discovery does not claim that guest configuration or provisioners ran.
	// A candidate persisted after the complete effect carries that evidence.
	if cfg, ok := n.Data.(*config.NodeConfig); ok && !incomplete && (len(cfg.Links) > 0 || len(cfg.Routes) > 0 || len(cfg.Provisioners) > 0) {
		return ResourceReadResult{Status: state.ResourceUnknown}, fmt.Errorf("%w: discovered node lacks complete attachment/guest action evidence", ErrActionUncertain)
	}
	if kind == "router" && !incomplete {
		return ResourceReadResult{Status: state.ResourceUnknown}, fmt.Errorf("%w: discovered router lacks policy action evidence", ErrActionUncertain)
	}
	if err := setDesiredHash(n, attrs); err != nil {
		return ResourceReadResult{}, err
	}
	r := state.Resource{Address: n.Address, Driver: provider, ExternalID: handle.ID, Attributes: state.MustAttributes(attrs), OwnerUID: identity.OwnerUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation}
	if incomplete {
		r.Attachments, err = discoverIntentAttachments(ctx, pc, n, handle, provider, nodeDriver)
		if err != nil {
			return ResourceReadResult{}, err
		}
	}
	blob, err := stateDriver.MarshalProviderState(handle)
	if err != nil {
		return ResourceReadResult{}, err
	}
	if len(blob) > 0 {
		if err := r.SetProviderState(blob); err != nil {
			return ResourceReadResult{}, err
		}
	}
	return checkedRead(ctx, NodeResourceHandler{}, r, pc.exec.state, pc.exec.providers)
}
