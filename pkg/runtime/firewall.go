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
)

type FirewallResourceHandler struct{}

func init() {
	RegisterResourceHandler(FirewallResourceHandler{})
}

func (FirewallResourceHandler) Type() string { return "sysbox_firewall" }

func (FirewallResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_firewall")
}

func (FirewallResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return resourceReadOK(current), nil
}

func (FirewallResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (FirewallResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.createFirewallResource(ctx, n)
}

func (FirewallResourceHandler) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	nsName := current.Str("netns")
	if nsName != "" {
		linuxNetwork, err := driver.DefaultRegistry.RequireLinuxNetwork("network")
		if err != nil {
			return err
		}
		if err := linuxNetwork.DeleteFirewall(context.Background(), nsName); err != nil {
			pc.Logf("[destroy] warning: delete firewall %s: %v\n", current.Address, err)
		}
	}
	pc.State().RemoveResource(current.Address)
	return nil
}

func (FirewallResourceHandler) ExternalID(current state.Resource) string {
	return current.Str("id")
}

func (FirewallResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.FirewallConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	if cfg.AttachTo != "" {
		ref, err := config.ResolveResourceAddress(cfg.AttachTo, "sysbox_network")
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, ref)
	}
	return cfg, deps, nil
}

func (e *Executor) createFirewallResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.FirewallConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("firewall %s: wrong data type", n.Address)
	}

	if cfg.AttachTo == "" {
		return state.Resource{}, fmt.Errorf("firewall %s: attach_to is empty", n.Address.Name)
	}
	netAddr, err := config.ResolveResourceAddress(cfg.AttachTo, "sysbox_network")
	if err != nil {
		return state.Resource{}, err
	}
	netState := e.state.FindResource(netAddr)
	if netState == nil {
		return state.Resource{}, fmt.Errorf("firewall %s: network %s not applied yet", n.Address.Name, netAddr)
	}
	nsName := netState.Str("netns")

	specs := make([]driver.FirewallRule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		specs = append(specs, driver.FirewallRule{
			Proto:  r.Proto,
			DPort:  r.DPort,
			SrcNet: r.SrcNet,
			Action: r.Action,
		})
	}

	linuxNetwork, err := driver.DefaultRegistry.RequireLinuxNetwork("network")
	if err != nil {
		return state.Resource{}, err
	}
	if err := linuxNetwork.ApplyFirewall(ctx, nsName, specs); err != nil {
		return state.Resource{}, fmt.Errorf("firewall %s: %w", n.Address.Name, err)
	}

	inst := map[string]any{
		"attach_to":  netAddr.String(),
		"netns":      nsName,
		"rules":      len(specs),
		"rule_specs": specs,
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
