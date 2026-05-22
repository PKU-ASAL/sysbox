package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
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

func (FirewallResourceProvider) Read(_ context.Context, current state.Resource) (state.Resource, error) {
	return current, nil
}

func (FirewallResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error) {
	return planDiffByDesiredHash(desired, current)
}

func (FirewallResourceProvider) Create(ctx context.Context, exec *Executor, n *graph.Node) (state.Resource, error) {
	return exec.createFirewallResource(ctx, n)
}

func (p FirewallResourceProvider) Update(ctx context.Context, exec *Executor, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, exec, desired)
}

func (FirewallResourceProvider) Delete(_ context.Context, exec *Executor, current state.Resource) error {
	nsName := current.Str("netns")
	if nsName != "" {
		if err := network.DeleteFirewall(nsName); err != nil {
			exec.logf("[destroy] warning: delete firewall %s: %v\n", current.Name, err)
		}
	}
	exec.state.RemoveResource(current.Type, current.Name)
	return nil
}

func (e *Executor) createFirewallResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.FirewallConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("firewall %s: wrong data type", n.ID)
	}

	netName := config.ResolveName(cfg.AttachTo)
	if netName == "" {
		return state.Resource{}, fmt.Errorf("firewall %s: attach_to is empty", n.ID.Name)
	}
	netState := e.state.FindResource("sysbox_network", netName)
	if netState == nil {
		return state.Resource{}, fmt.Errorf("firewall %s: network %s not applied yet", n.ID.Name, netName)
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
		return state.Resource{}, fmt.Errorf("firewall %s: %w", n.ID.Name, err)
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
		Type:     "sysbox_firewall",
		Name:     n.ID.Name,
		Provider: "network",
		Instance: inst,
	}, nil
}
