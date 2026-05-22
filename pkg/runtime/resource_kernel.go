package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

// createKernel resolves a sysbox_kernel resource into a local on-disk path
// via the artifact resolver (downloading + caching as needed) and records it
// in state. Other resources (sysbox_node) reference the resolved path by
// looking up state["sysbox_kernel", name].path.
func (e *Executor) createKernel(ctx context.Context, n *graph.Node) error {
	p := mustResourceProvider("sysbox_kernel")
	res, err := p.Create(ctx, e, n)
	if err != nil {
		return err
	}
	e.state.AddResource(res)
	return nil
}

type KernelResourceProvider struct{}

func init() {
	RegisterResourceProvider(KernelResourceProvider{})
}

func (KernelResourceProvider) Type() string { return "sysbox_kernel" }

func (KernelResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_kernel")
}

func (KernelResourceProvider) Read(_ context.Context, current state.Resource) (state.Resource, error) {
	return current, nil
}

func (p KernelResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error) {
	if current == nil {
		return PlanAction{
			Resource: desired.ID.String(),
			Type:     desired.ID.Type,
			Name:     desired.ID.Name,
			Action:   PlanActionCreate,
			Reason:   "resource not present in state",
		}, nil
	}
	action := PlanActionNoop
	reason := ""
	var changes map[string]FieldChange
	if stateDesiredHash(current) != "" {
		want, err := desiredHash(desired)
		if err != nil {
			return PlanAction{}, err
		}
		if want != stateDesiredHash(current) {
			changes, reason = diffDesiredState(desired, current)
			action = PlanActionReplace
		}
	}
	if action == PlanActionReplace && reason == "" {
		action = PlanActionReplace
		reason = "desired configuration changed; replacement required"
	}
	return PlanAction{
		Resource: desired.ID.String(),
		Type:     desired.ID.Type,
		Name:     desired.ID.Name,
		Action:   action,
		Reason:   reason,
		Changes:  changes,
	}, nil
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
