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
	return ResourceReadResult{Status: state.ResourceUnknown, Resource: current}, fmt.Errorf("%w: ssh_access read requires host connection context", ErrConvergenceUnsupported)
}

func (SSHAccessResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	if desired == nil {
		return controlplane.PlannedChange{}, fmt.Errorf("ssh_access plan requires desired config")
	}
	cfg, ok := desired.Data.(*config.SSHAccessConfig)
	if !ok {
		return controlplane.PlannedChange{}, fmt.Errorf("ssh_access plan has invalid config")
	}
	keys := append([]string(nil), cfg.AuthorizedKeys...)
	nodeRef, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
	if err != nil {
		return controlplane.PlannedChange{}, err
	}
	req := controlplane.SSHAccessRequest{ID: desired.Address.Name, AccessID: desired.Address.Name, Topology: "topology", Node: nodeRef.Name, AuthorizedKeys: keys, BindIP: cfg.BindIP, Port: cfg.Port}
	if err := req.Validate(); err != nil {
		return controlplane.PlannedChange{}, err
	}
	return planDiffByDesiredHash(desired, current)
}

func (SSHAccessResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	if pc == nil || pc.exec == nil || n == nil {
		return state.Resource{}, fmt.Errorf("%w: ssh_access requires a managed guest configuration contract", ErrConvergenceUnsupported)
	}
	cfg, ok := n.Data.(*config.SSHAccessConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("invalid ssh_access config")
	}
	nodeRef, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
	if err != nil {
		return state.Resource{}, err
	}
	req := controlplane.SSHAccessRequest{ID: n.Address.Name, AccessID: n.Address.Name, Topology: pc.Topology(), Node: nodeRef.Name, AuthorizedKeys: append([]string(nil), cfg.AuthorizedKeys...), BindIP: cfg.BindIP, Port: cfg.Port}
	if err := req.Validate(); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{}, fmt.Errorf("%w: ssh_access is a remote node operation; use the API/Agent host supervisor route", ErrConvergenceUnsupported)
}

func (SSHAccessResourceHandler) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	if pc == nil || pc.exec == nil {
		return fmt.Errorf("%w: managed SSH context required", ErrConvergenceUnsupported)
	}
	node := current.Str("node")
	path := current.Str("path")
	if node == "" || path == "" {
		return fmt.Errorf("%w: invalid managed ssh access state", ErrConvergenceUnsupported)
	}
	nodeRef, err := config.ResolveResourceAddress(node, "sysbox_node")
	if err != nil {
		return err
	}
	user, err := pc.SSHUserForNode(nodeRef.String())
	if err != nil {
		return err
	}
	expected, err := controlplane.ManagedSSHPath(user, current.Address.Name)
	if err != nil || path != expected || current.Address.Type != "sysbox_ssh_access" || current.OwnerUID == "" || current.OwnerUID != pc.State().TopologyUID {
		return fmt.Errorf("%w: managed SSH identity mismatch", ErrConvergenceUnsupported)
	}
	return fmt.Errorf("%w: ssh_access is a remote node operation; use the API/Agent host supervisor route", ErrConvergenceUnsupported)
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
