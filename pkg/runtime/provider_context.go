package runtime

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type ProviderContext struct {
	exec *Executor
}

func (c *ProviderContext) State() *state.State {
	return c.exec.state
}

func (c *ProviderContext) Topology() string {
	return c.exec.topology
}

func (c *ProviderContext) RunID() string {
	return c.exec.runID
}

func (c *ProviderContext) CurrentResourceStep() int {
	return c.exec.currentResourceStep
}

func (c *ProviderContext) Logf(format string, args ...any) {
	fmt.Fprintf(c.exec.logger, format, args...)
}

func (c *ProviderContext) RecordSubstep(parent int, phase string, details map[string]any, fn func() error) error {
	return c.exec.recordSubstep(parent, phase, details, fn)
}

func (c *ProviderContext) SubstepHook(parent int) NICWireHook {
	return c.exec.substepHook(parent)
}

// ConnectionForNode resolves a canonical node resource and its declared
// connection hints. Resource handlers cannot provide an endpoint or provider
// handle through this method; both come from the executor's graph/state.
func (c *ProviderContext) ConnectionForNode(ctx context.Context, ref string) (substrate.Connection, error) {
	addr, err := address.Parse(ref)
	if err != nil {
		return nil, err
	}
	node := c.exec.graph.Get(addr)
	if node == nil {
		return nil, fmt.Errorf("node %s not found", addr)
	}
	cfg, ok := node.Data.(*config.NodeConfig)
	if !ok {
		return nil, fmt.Errorf("node %s has invalid config", addr)
	}
	resource := c.exec.state.FindResource(addr)
	if resource == nil {
		return nil, fmt.Errorf("node %s is not present in state", addr)
	}
	nd, err := driver.DefaultRegistry.RequireNode(resource.Driver)
	if err != nil {
		return nil, err
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(resource.Driver)
	if err != nil {
		return nil, err
	}
	h, err := resource.ReconstructHandle(codec)
	if err != nil {
		return nil, err
	}
	return c.exec.connectionForNode(ctx, nd, h, cfg.Connections)
}

func (c *ProviderContext) createNodeResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.createNodeResource(ctx, n)
}

func (c *ProviderContext) createRouterResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.createRouterResource(ctx, n)
}

func (c *ProviderContext) createFirewallResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.createFirewallResource(ctx, n)
}

func (c *ProviderContext) readDataNodeResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.readDataNodeResource(ctx, n)
}

func (c *ProviderContext) readDataNetworkResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.readDataNetworkResource(ctx, n)
}

func (c *ProviderContext) readDataImageResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.readDataImageResource(ctx, n)
}

func (c *ProviderContext) destroyNodeResource(ctx context.Context, r state.Resource) error {
	return c.exec.destroyNodeResource(ctx, r)
}
