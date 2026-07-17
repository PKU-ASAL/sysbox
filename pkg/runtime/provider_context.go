package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
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

func (c *ProviderContext) createNodeResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.createNodeResource(ctx, n)
}

func (c *ProviderContext) createRouterResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.createRouterResource(ctx, n)
}

func (c *ProviderContext) createSSHAccessResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return c.exec.createSSHAccessResource(ctx, n)
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
