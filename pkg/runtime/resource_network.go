package runtime

import (
	"context"
	"fmt"
	"net"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type NetworkResourceHandler struct{}

func init() {
	RegisterResourceHandler(NetworkResourceHandler{})
}

func (NetworkResourceHandler) Type() string { return "sysbox_network" }

func (NetworkResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_network")
}

func (NetworkResourceHandler) Read(ctx context.Context, current state.Resource, providers *driver.Registry) (ResourceReadResult, error) {
	result := resourceReadOK(current)
	if current.IsNAT() {
		networkDriver, err := providers.RequireNetwork(current.Driver)
		if err != nil {
			return ResourceReadResult{Status: state.ResourceUnknown}, err
		}
		name := current.DockerNetID()
		if name == "" {
			return ResourceReadResult{Status: state.ResourceUnknown}, fmt.Errorf("missing managed network identity")
		}
		info, err := networkDriver.ReadManagedNetwork(ctx, substrate.ManagedNetworkSpec{Name: name})
		if driver.IsCategory(err, driver.ErrorNotFound) {
			result.Status = state.ResourceAbsent
			return result, nil
		}
		if err != nil {
			result.Status = state.ResourceUnknown
			return result, err
		}
		if info.ID != name {
			result.Status = state.ResourceUnknown
			return result, fmt.Errorf("managed network identity conflict")
		}
		result.Checks = map[string]model.ResourceCheckHealth{"docker_network": {OK: true}}
		return result, nil
	}
	linuxNetwork, err := providers.RequireLinuxNetwork("network")
	if err != nil {
		result.Status = state.ResourceUnknown
		return result, err
	}
	observer, ok := linuxNetwork.(driver.IsolatedNetworkObserver)
	if !ok {
		return ResourceReadResult{Status: state.ResourceUnknown}, fmt.Errorf("%w: isolated network driver lacks existence observation", ErrConvergenceUnsupported)
	}
	if _, ok := substrate.OperationIdentityFromContext(ctx); !ok && current.OwnerUID != "" {
		ctx = substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "observe", TopologyUID: current.OwnerUID, OwnerUID: current.OwnerUID, ResourceUID: current.ResourceUID, Generation: current.Generation})
	}
	observation, err := observer.ObserveIsolated(ctx, isolatedNetworkSpec(current))
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown, Resource: current}, err
	}
	result.Reason = observation.Reason
	result.Checks = map[string]model.ResourceCheckHealth{"network": {OK: observation.Healthy, Reason: observation.Reason}}
	if !observation.Exists {
		result.Status = state.ResourceAbsent
	} else if !observation.Healthy {
		result.Status = state.ResourceDrifted
	}
	return result, nil
}

func (NetworkResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (model.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (NetworkResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.NetworkConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("network %s: wrong data type", n.Address)
	}
	pc.Logf("[network %s] create cidr=%s nat=%t\n", n.Address, cfg.CIDR, cfg.NAT)

	// nat=true: use Docker's bridge driver for internet access.
	if cfg.NAT {
		return createNATNetwork(ctx, pc, n, cfg)
	}

	// Default: isolated netns/bridge/veth topology.
	networkName := networkExternalName(pc.Topology(), n.Address)
	nsName := fmt.Sprintf("sysbox-net-%s", networkName)
	brName := shortLinuxName("br", networkName)
	libvirtBridge := shortLinuxName("lv", networkName)
	rootEnd := shortLinuxName("lvr", networkName)
	namespaceEnd := shortLinuxName("lvn", networkName)
	gwCIDR, err := gatewayCIDR(cfg.CIDR)
	if err != nil {
		return state.Resource{}, err
	}
	linuxNetwork, err := pc.exec.providers.RequireLinuxNetwork("network")
	if err != nil {
		return state.Resource{}, err
	}
	rootRoutes := make([]driver.HostRouteSpec, 0, len(cfg.RootRoutes))
	for _, route := range cfg.RootRoutes {
		rootRoutes = append(rootRoutes, driver.HostRouteSpec{Destination: route.Destination, Via: route.Via})
	}
	if err := linuxNetwork.CreateIsolated(ctx, driver.IsolatedNetworkSpec{
		Name:         nsName,
		Bridge:       brName,
		CIDR:         gwCIDR,
		RootBridge:   libvirtBridge,
		RootEnd:      rootEnd,
		NamespaceEnd: namespaceEnd,
		RootAddress:  cfg.RootAddress,
		RootRoutes:   rootRoutes,
	}); err != nil {
		return state.Resource{}, err
	}

	inst := map[string]any{
		"netns":          nsName,
		"bridge":         brName,
		"cidr":           cfg.CIDR,
		"gateway":        gwCIDR,
		"libvirt_bridge": libvirtBridge,
		"root_veth":      rootEnd,
		"netns_veth":     namespaceEnd,
		"root_address":   cfg.RootAddress,
	}
	if len(rootRoutes) > 0 {
		routeSpecs := make([]map[string]string, 0, len(rootRoutes))
		for _, route := range rootRoutes {
			routeSpecs = append(routeSpecs, map[string]string{"dst": route.Destination, "via": route.Via})
		}
		inst["root_routes"] = routeSpecs
	}
	if lc := cfg.Lifecycle; lc != nil {
		inst["lifecycle_prevent_destroy"] = lc.PreventDestroy
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{
		Address:    n.Address,
		Driver:     "network",
		Attributes: state.MustAttributes(inst),
	}, nil
}

// createNATNetwork creates a managed NAT network via the registered substrate.
// Currently Docker is the only substrate that supports managed networks.
func createNATNetwork(ctx context.Context, pc *ProviderContext, n *graph.Node, cfg *config.NetworkConfig) (state.Resource, error) {
	cfg.ExposeBridge = cfg.ExposeBridge || requiresManagedBridge(pc.exec.graph, n.Address)
	networkDriver, err := pc.exec.providers.RequireNetwork("docker")
	if err != nil {
		return state.Resource{}, fmt.Errorf("nat network requires docker substrate: %w", err)
	}

	info, err := networkDriver.CreateManagedNetwork(ctx, substrate.ManagedNetworkSpec{
		ExposeBridge: cfg.ExposeBridge,
		Name:         networkExternalName(pc.Topology(), n.Address),
		CIDR:         cfg.CIDR,
		NAT:          true,
		Labels:       ManagedLabels(pc.Topology(), pc.RunID(), n.Address),
	})
	if err != nil {
		return state.Resource{}, fmt.Errorf("create nat network %s: %w", n.Address.Name, err)
	}

	// Ensure Docker containers on this NAT network can reach the internet.
	// Some hosts have restrictive DOCKER-USER iptables policies (e.g. campus
	// networks that DROP outbound HTTP). Insert ACCEPT rules for the NAT
	// subnet before any DROP rules so container traffic is allowed.
	if err := networkDriver.AllowEgress(ctx, cfg.CIDR); err != nil {
		pc.Logf("[network %s] warning: could not add DOCKER-USER ACCEPT for %s: %v\n",
			n.Address.Name, cfg.CIDR, err)
	}

	natInst := map[string]any{
		"nat":               true,
		"docker_network_id": info.ID,
		"docker_net_name":   info.Name,
		"cidr":              cfg.CIDR,
	}
	if lc := cfg.Lifecycle; lc != nil {
		natInst["lifecycle_prevent_destroy"] = lc.PreventDestroy
	}
	if err := setDesiredHash(n, natInst); err != nil {
		return state.Resource{}, err
	}
	result := state.Resource{Address: n.Address, Driver: "docker", Attributes: state.MustAttributes(natInst)}
	if info.BridgeTarget != nil {
		if err := result.SetRuntimeValue("managed_bridge", info.BridgeTarget); err != nil {
			return state.Resource{}, err
		}
	}
	return result, nil
}

func (NetworkResourceHandler) Delete(ctx context.Context, pc *ProviderContext, r state.Resource) error {
	if r.IsNAT() {
		networkDriver, err := pc.exec.providers.RequireNetwork("docker")
		if err != nil {
			return err
		}
		netID := r.DockerNetID()
		if netID != "" {
			if err := networkDriver.RemoveManagedNetwork(ctx, netID); err != nil {
				// Do not swallow this: removing the state entry while the docker
				// network still exists leaves an orphan that can never be cleaned
				// up, and the next apply of the same CIDR fails with "Pool
				// overlaps". Returning the error keeps the resource in state so a
				// later destroy retries the removal.
				return fmt.Errorf("remove bridge network %s: %w", netID, err)
			}
		}
		// Clean up DOCKER-USER ACCEPT rules for this NAT subnet.
		if cidr := r.Str("cidr"); cidr != "" {
			_ = networkDriver.RemoveEgress(ctx, cidr)
		}
		pc.State().RemoveResource(r.Address)
		return nil
	}

	nsName := r.Str("netns")
	linuxNetwork, err := pc.exec.providers.RequireLinuxNetwork("network")
	if err != nil {
		return err
	}
	if err := linuxNetwork.DeleteIsolated(ctx, isolatedNetworkSpec(r)); err != nil {
		return fmt.Errorf("delete netns %s: %w", nsName, err)
	}
	pc.State().RemoveResource(r.Address)
	return nil
}

func (NetworkResourceHandler) ExternalID(current state.Resource) string {
	if id := current.DockerNetID(); id != "" {
		return id
	}
	if ns := current.NetNS(); ns != "" {
		return ns
	}
	return current.Str("id")
}

func isolatedNetworkSpec(resource state.Resource) driver.IsolatedNetworkSpec {
	rootRoutes := make([]driver.HostRouteSpec, 0)
	for _, item := range resource.Slice("root_routes") {
		route, ok := item.(map[string]any)
		if !ok {
			continue
		}
		destination, _ := route["dst"].(string)
		via, _ := route["via"].(string)
		if destination != "" && via != "" {
			rootRoutes = append(rootRoutes, driver.HostRouteSpec{Destination: destination, Via: via})
		}
	}
	return driver.IsolatedNetworkSpec{
		Name:         resource.Str("netns"),
		Bridge:       resource.Str("bridge"),
		CIDR:         resource.Str("gateway"),
		RootBridge:   resource.Str("libvirt_bridge"),
		RootEnd:      resource.Str("root_veth"),
		NamespaceEnd: resource.Str("netns_veth"),
		RootAddress:  resource.Str("root_address"),
		RootRoutes:   rootRoutes,
	}
}
func (NetworkResourceHandler) RequiredCapabilities(node *graph.Node, providers *driver.Registry) ([]CapabilityRequirement, error) {
	cfg, ok := node.Data.(*config.NetworkConfig)
	if !ok {
		return nil, nil
	}
	if cfg.NAT {
		return []CapabilityRequirement{{"docker", driver.CapabilityNetwork}}, nil
	}
	return []CapabilityRequirement{{"network", driver.CapabilityLinuxNetwork}}, nil
}

func gatewayCIDR(cidr string) (string, error) {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	ones, _ := network.Mask.Size()
	address := append(net.IP(nil), ip...)
	address[len(address)-1]++
	return fmt.Sprintf("%s/%d", address.String(), ones), nil
}

func (NetworkResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext, providers *driver.Registry) (any, []address.Address, error) {
	cfg := &config.NetworkConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	return cfg, nil, nil
}
