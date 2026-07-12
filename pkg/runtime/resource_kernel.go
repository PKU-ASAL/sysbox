package runtime

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type KernelResourceHandler struct{}

func init() {
	RegisterResourceHandler(KernelResourceHandler{})
}

func (KernelResourceHandler) Type() string { return "sysbox_kernel" }

func (KernelResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_kernel")
}

func (KernelResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	result := resourceReadOK(current)
	path := current.Str("path")
	if path == "" {
		result.Checks = map[string]controlplane.ResourceCheckHealth{"file": {OK: false, Reason: "kernel path missing from state"}}
		result.Status = state.ResourceDrifted
		result.Reason = "kernel path missing from state"
		return result, nil
	}
	if _, err := os.Stat(path); err != nil {
		result.Checks = map[string]controlplane.ResourceCheckHealth{"file": {OK: false, Reason: err.Error()}}
		result.Status = state.ResourceDrifted
		result.Reason = err.Error()
		return result, nil
	}
	result.Checks = map[string]controlplane.ResourceCheckHealth{"file": {OK: true}}
	return result, nil
}

func (p KernelResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (KernelResourceHandler) Create(_ context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.KernelConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("kernel %s: wrong data type", n.Address)
	}
	if cfg.Source == "" {
		return state.Resource{}, fmt.Errorf("kernel %s: source required", n.Address.Name)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, err
	}

	res, err := artifact.New().Resolve(artifact.Spec{Source: cfg.Source, SHA256: cfg.SHA256})
	if err != nil {
		return state.Resource{}, fmt.Errorf("kernel %s: %w", n.Address.Name, err)
	}
	if res.FromCache {
		pc.Logf("[apply] kernel %s: cache hit (%s)\n", n.Address.Name, res.Path)
	} else if artifact.IsURL(cfg.Source) {
		pc.Logf("[apply] kernel %s: fetched to %s\n", n.Address.Name, res.Path)
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
		Address:    n.Address,
		Driver:     subName,
		Attributes: state.MustAttributes(inst),
	}, nil
}

func (KernelResourceHandler) Delete(_ context.Context, pc *ProviderContext, current state.Resource) error {
	pc.State().RemoveResource(current.Address)
	return nil
}

func (KernelResourceHandler) ExternalID(current state.Resource) string {
	if path := current.Str("path"); path != "" {
		return path
	}
	return current.Str("id")
}

func (KernelResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.KernelConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	deps, err := decodeDependsOn(nil, cfg.DependsOn)
	return cfg, deps, err
}

func (KernelResourceHandler) PreflightResource(r config.ResourceBlock, ctx *hcl.EvalContext) []substrate.PreflightCheck {
	cfg := &config.KernelConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return []substrate.PreflightCheck{DecodePreflightError(r.Type, r.Name, err)}
	}
	if check := ArtifactPreflightCheck("kernel:"+r.Name, cfg.Source, cfg.SHA256); check != nil {
		return []substrate.PreflightCheck{*check}
	}
	return nil
}
