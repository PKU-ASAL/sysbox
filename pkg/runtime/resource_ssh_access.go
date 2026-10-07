package runtime

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

// -- sysbox_ssh_access --

type SSHAccessResourceHandler struct{}

func init() {
	RegisterResourceHandler(SSHAccessResourceHandler{})
}

func (SSHAccessResourceHandler) Type() string { return "sysbox_ssh_access" }

func (SSHAccessResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_ssh_access")
}

func (SSHAccessResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return ResourceReadResult{Status: state.ResourceUnknown, Resource: current}, fmt.Errorf("%w: ssh_access lacks read-back verification", ErrConvergenceUnsupported)
}

func (SSHAccessResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (SSHAccessResourceHandler) Create(context.Context, *ProviderContext, *graph.Node) (state.Resource, error) {
	return state.Resource{}, fmt.Errorf("%w: ssh_access requires a managed guest configuration contract", ErrConvergenceUnsupported)
}

func (SSHAccessResourceHandler) Delete(context.Context, *ProviderContext, state.Resource) error {
	return fmt.Errorf("%w: ssh_access deletion requires verified guest cleanup", ErrConvergenceUnsupported)
}

func (SSHAccessResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (SSHAccessResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.SSHAccessConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	if cfg.Node != "" {
		ref, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, ref)
	}
	return cfg, deps, nil
}
