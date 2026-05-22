package runtime

import (
	"context"
	"fmt"
	"os"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

type KernelResourceProvider struct{}

func init() {
	RegisterResourceProvider(KernelResourceProvider{})
}

func (KernelResourceProvider) Type() string { return "sysbox_kernel" }

func (KernelResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_kernel")
}

func (KernelResourceProvider) Read(_ context.Context, current state.Resource) (state.Resource, error) {
	path := current.Str("path")
	if path == "" {
		return current, driftedResource("kernel path missing from state")
	}
	if _, err := os.Stat(path); err != nil {
		return current, driftedResource(err.Error())
	}
	return current, nil
}

func (p KernelResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error) {
	return planDiffByDesiredHash(desired, current)
}

func (KernelResourceProvider) Create(_ context.Context, exec *Executor, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.KernelConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("kernel %s: wrong data type", n.ID)
	}
	if cfg.Source == "" {
		return state.Resource{}, fmt.Errorf("kernel %s: source required", n.ID.Name)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, err
	}

	res, err := artifact.New().Resolve(artifact.Spec{Source: cfg.Source, SHA256: cfg.SHA256})
	if err != nil {
		return state.Resource{}, fmt.Errorf("kernel %s: %w", n.ID.Name, err)
	}
	if res.FromCache {
		exec.logf("[apply] kernel %s: cache hit (%s)\n", n.ID.Name, res.Path)
	} else if artifact.IsURL(cfg.Source) {
		exec.logf("[apply] kernel %s: fetched to %s\n", n.ID.Name, res.Path)
	}

	inst := map[string]any{
		"path":             res.Path,
		"source":           cfg.Source,
		"sha256":           res.SHA256,
		"cmdline_template": cfg.CmdlineTemplate,
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{
		Type:     "sysbox_kernel",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: inst,
	}, nil
}

func (p KernelResourceProvider) Update(ctx context.Context, exec *Executor, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, exec, desired)
}

func (KernelResourceProvider) Delete(_ context.Context, exec *Executor, current state.Resource) error {
	exec.state.RemoveResource(current.Type, current.Name)
	return nil
}
