package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/util"
)

func (e *Executor) createFirewall(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.FirewallConfig)
	if !ok {
		return fmt.Errorf("firewall %s: wrong data type", n.ID)
	}

	netName := config.ResolveName(cfg.AttachTo)
	if netName == "" {
		return fmt.Errorf("firewall %s: attach_to is empty", n.ID.Name)
	}
	netState := e.state.FindResource("sysbox_network", netName)
	if netState == nil {
		return fmt.Errorf("firewall %s: network %s not applied yet", n.ID.Name, netName)
	}
	nsName := util.AsString(netState.Instance["netns"])

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
		return fmt.Errorf("firewall %s: %w", n.ID.Name, err)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_firewall",
		Name:     n.ID.Name,
		Provider: "network",
		Instance: map[string]any{
			"attach_to":  netName,
			"netns":      nsName,
			"rules":      len(specs),
			"rule_specs": specs,
		},
	})
	return nil
}

func (e *Executor) destroyFirewall(ctx context.Context, r state.Resource) error {
	nsName := r.Str("netns")
	if nsName != "" {
		if err := network.DeleteFirewall(nsName); err != nil {
			e.logf("[destroy] warning: delete firewall %s: %v\n", r.Name, err)
		}
	}
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}
