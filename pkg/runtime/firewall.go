package runtime

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
)

type FirewallResourceProvider struct{}

func init() {
	RegisterResourceProvider(FirewallResourceProvider{})
}

func (FirewallResourceProvider) Type() string { return "sysbox_firewall" }

func (FirewallResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_firewall")
}

func (FirewallResourceProvider) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return resourceReadOK(current), nil
}

func (FirewallResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	return planDiffByDesiredHash(desired, current)
}

func (FirewallResourceProvider) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.createFirewallResource(ctx, n)
}

func (p FirewallResourceProvider) Update(ctx context.Context, pc *ProviderContext, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, pc, desired)
}

func (FirewallResourceProvider) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	nsName := current.Str("netns")
	if nsName != "" {
		if err := network.DeleteFirewall(nsName); err != nil {
			pc.Logf("[destroy] warning: delete firewall %s: %v\n", current.Address, err)
		}
	}
	pc.State().RemoveResource(current.Address)
	return nil
}

func (FirewallResourceProvider) ExternalID(current state.Resource) string {
	return current.Str("id")
}

func (FirewallResourceProvider) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.FirewallConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	if ref := config.ResolveName(cfg.AttachTo); ref != "" {
		deps = append(deps, address.Address{Type: "sysbox_network", Name: ref})
	}
	return cfg, deps, nil
}

func (e *Executor) createFirewallResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.FirewallConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("firewall %s: wrong data type", n.Address)
	}

	netName := config.ResolveName(cfg.AttachTo)
	if netName == "" {
		return state.Resource{}, fmt.Errorf("firewall %s: attach_to is empty", n.Address.Name)
	}
	netState := e.state.FindResource(address.Resource("sysbox_network", netName))
	if netState == nil {
		return state.Resource{}, fmt.Errorf("firewall %s: network %s not applied yet", n.Address.Name, netName)
	}
	nsName := netState.Str("netns")

	specs := make([]network.FirewallRuleSpec, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		specs = append(specs, network.FirewallRuleSpec{
			Proto:  r.Proto,
			DPort:  r.DPort,
			SrcNet: r.SrcNet,
			Action: r.Action,
		})
	}

	if err := network.ApplyFirewall(nsName, specs); err != nil {
		return state.Resource{}, fmt.Errorf("firewall %s: %w", n.Address.Name, err)
	}

	inst := map[string]any{
		"attach_to":  netName,
		"netns":      nsName,
		"rules":      len(specs),
		"rule_specs": specs,
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{
		Address:  n.Address,
		Provider: "network",
		Instance: inst,
	}, nil
}
