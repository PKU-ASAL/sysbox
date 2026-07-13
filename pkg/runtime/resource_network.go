package runtime

import (
	"context"
	"fmt"
	"net"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
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

func (NetworkResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	result := resourceReadOK(current)
	if current.IsNAT() {
		result.Checks = map[string]controlplane.ResourceCheckHealth{"docker_network": {OK: true}}
		return result, nil
	}
	nsName := current.Str("netns")
	brName := current.Str("bridge")
	if nsName == "" {
		result.Reason = "network has no isolated namespace"
		return result, nil
	}
	linuxNetwork, err := driver.DefaultRegistry.RequireLinuxNetwork("network")
	if err != nil {
		result.Status = state.ResourceUnknown
		return result, err
	}
	ok, reason := linuxNetwork.NetworkHealthy(context.Background(), driver.IsolatedNetworkSpec{Name: nsName, Bridge: brName})
	checks := map[string]controlplane.ResourceCheckHealth{"network": {OK: ok, Reason: reason}}
	if !ok {
		result.Checks = checks
		result.Status = state.ResourceDrifted
		result.Reason = reason
		return result, nil
	}
	result.Checks = checks
	return result, nil
}

func (NetworkResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (NetworkResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.NetworkConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("network %s: wrong data type", n.Address)
	}

	// nat=true: use Docker's bridge driver for internet access.
	if cfg.NAT {
		return createNATNetwork(ctx, pc, n, cfg)
	}

	// Default: isolated netns/bridge/veth topology.
	networkName := networkExternalName(pc.Topology(), n.Address.Name)
	nsName := fmt.Sprintf("sysbox-net-%s", networkName)
	brName := shortLinuxName("br", networkName)
	gwCIDR, err := gatewayCIDR(cfg.CIDR)
	if err != nil {
		return state.Resource{}, err
	}
	linuxNetwork, err := driver.DefaultRegistry.RequireLinuxNetwork("network")
	if err != nil {
		return state.Resource{}, err
	}
	if err := linuxNetwork.CreateIsolated(ctx, driver.IsolatedNetworkSpec{Name: nsName, Bridge: brName, CIDR: gwCIDR}); err != nil {
		return state.Resource{}, err
	}

	inst := map[string]any{
		"netns":   nsName,
		"bridge":  brName,
		"cidr":    cfg.CIDR,
		"gateway": gwCIDR,
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
	networkDriver, err := driver.DefaultRegistry.RequireNetwork("docker")
	if err != nil {
		return state.Resource{}, fmt.Errorf("nat network requires docker substrate: %w", err)
	}

	info, err := networkDriver.CreateManagedNetwork(ctx, substrate.ManagedNetworkSpec{
		Name:   networkExternalName(pc.Topology(), n.Address.Name),
		CIDR:   cfg.CIDR,
		NAT:    true,
		Labels: ManagedLabels(pc.Topology(), pc.RunID(), n.Address),
	})
	if err != nil {
		return state.Resource{}, fmt.Errorf("create nat network %s: %w", n.Address.Name, err)
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
	return state.Resource{
		Address:    n.Address,
		Driver:     "docker",
		Attributes: state.MustAttributes(natInst),
	}, nil
}

func (NetworkResourceHandler) Delete(ctx context.Context, pc *ProviderContext, r state.Resource) error {
	if r.IsNAT() {
		networkDriver, err := driver.DefaultRegistry.RequireNetwork("docker")
		if err != nil {
			pc.State().RemoveResource(r.Address)
			return nil
		}
		netID := r.DockerNetID()
		if netID != "" {
			if err := networkDriver.RemoveManagedNetwork(ctx, netID); err != nil {
				pc.Logf("[destroy] warning: remove bridge network %s: %v\n", netID, err)
			}
		}
		pc.State().RemoveResource(r.Address)
		return nil
	}

	nsName := r.Str("netns")
	brName := r.Str("bridge")
	linuxNetwork, err := driver.DefaultRegistry.RequireLinuxNetwork("network")
	if err != nil {
		return err
	}
	if err := linuxNetwork.DeleteIsolated(ctx, driver.IsolatedNetworkSpec{Name: nsName, Bridge: brName}); err != nil {
		pc.Logf("[destroy] warning: delete netns %s: %v\n", nsName, err)
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
func (NetworkResourceHandler) RequiredCapabilities(node *graph.Node) ([]CapabilityRequirement, error) {
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

func (NetworkResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.NetworkConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	return cfg, nil, nil
}
