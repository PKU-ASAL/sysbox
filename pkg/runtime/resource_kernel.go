package runtime

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"

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

func (KernelResourceProvider) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	result := resourceReadOK(current)
	path := current.Str("path")
	if path == "" {
		result.Checks = map[string]ResourceCheckHealth{"file": {OK: false, Reason: "kernel path missing from state"}}
		return result, driftedResource("kernel path missing from state")
	}
	if _, err := os.Stat(path); err != nil {
		result.Checks = map[string]ResourceCheckHealth{"file": {OK: false, Reason: err.Error()}}
		return result, driftedResource(err.Error())
	}
	result.Checks = map[string]ResourceCheckHealth{"file": {OK: true}}
	return result, nil
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

func (KernelResourceProvider) ExternalID(current state.Resource) string {
	if path := current.Str("path"); path != "" {
		return path
	}
	return current.Str("id")
}

func (KernelResourceProvider) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []graph.Ref, error) {
	cfg := &config.KernelConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	return cfg, decodeDependsOn(nil, cfg.DependsOn), nil
}

func (KernelResourceProvider) PreflightResource(r config.ResourceBlock, ctx *hcl.EvalContext) []PreflightCheck {
	cfg := &config.KernelConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return []PreflightCheck{DecodePreflightError(r.Type, r.Name, err)}
	}
	if check := ArtifactPreflightCheck("kernel:"+r.Name, cfg.Source, cfg.SHA256); check != nil {
		return []PreflightCheck{*check}
	}
	return nil
}
