package runtime

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type SSHAccessResourceHandler struct{}

func init()                                   { RegisterResourceHandler(SSHAccessResourceHandler{}) }
func (SSHAccessResourceHandler) Type() string { return "sysbox_ssh_access" }
func (SSHAccessResourceHandler) Schema() ResourceSchema {
	s := ResourceSchemaFor("sysbox_ssh_access")
	s.Version = 2
	return s
}
func (SSHAccessResourceHandler) Read(_ context.Context, current state.Resource, providers *driver.Registry) (ResourceReadResult, error) {
	return ResourceReadResult{Status: state.ResourceUnknown, Resource: current}, fmt.Errorf("%w: SSH access observation requires canonical node state", ErrConvergenceUnsupported)
}

func (SSHAccessResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (model.PlannedChange, error) {
	if desired == nil {
		return model.PlannedChange{}, fmt.Errorf("SSH access desired config required")
	}
	_, _, err := sshDesired(desired, "validation", "validation")
	if err != nil {
		return model.PlannedChange{}, err
	}
	return planDiffByDesiredHash(desired, current)
}

func (SSHAccessResourceHandler) ReadWithState(ctx context.Context, current state.Resource, st *state.State, providers *driver.Registry) (ResourceReadResult, error) {
	result := ResourceReadResult{Status: state.ResourceUnknown, Resource: current}
	saved, err := decodeSSHState(current, st)
	if err != nil {
		return result, err
	}
	ctx, connection, query, err := sshNodeConnection(ctx, st, saved.Node, saved.Handle.AccessID, providers)
	if err != nil {
		return result, err
	}
	if err = validateSSHStateTarget(saved, query); err != nil {
		return result, err
	}
	observation, err := connection.InspectSSHAccess(ctx, saved.Handle)
	if err != nil {
		return result, err
	}
	if observation.Revision < saved.Handle.Revision {
		return result, fmt.Errorf("SSH revision regressed")
	}
	switch observation.Status {
	case "present":
		if observation.Revision != saved.Handle.Revision {
			return result, fmt.Errorf("SSH current revision mismatch")
		}
		result.Status = state.ResourcePresent
	case "absent":
		result.Status = state.ResourceAbsent
	case "drifted":
		result.Status = state.ResourceDrifted
	default:
		return result, fmt.Errorf("SSH current configuration unknown")
	}
	return result, nil
}

func (h SSHAccessResourceHandler) Discover(ctx context.Context, pc *ProviderContext, n *graph.Node) (ResourceReadResult, error) {
	if pc == nil || pc.exec == nil || n == nil {
		return ResourceReadResult{Status: state.ResourceUnknown}, ErrConvergenceUnsupported
	}
	ref, _, err := sshDesired(n, "validation", "validation")
	if err != nil {
		return ResourceReadResult{}, err
	}
	node, err := address.Parse(ref)
	if err != nil {
		return ResourceReadResult{}, err
	}
	// Preflight precedes creation of dependency nodes. Action-boundary discovery
	// resolves again after the node is present.
	if pc.State().FindResource(node) == nil {
		return ResourceReadResult{Status: state.ResourceAbsent}, nil
	}
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("SSH discovery requires action identity")
	}
	nodeCtx, connection, query, err := sshNodeConnection(ctx, pc.State(), ref, n.Address.Name, pc.exec.providers)
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown}, err
	}
	found, err := connection.DiscoverSSHAccess(nodeCtx, query)
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown}, err
	}
	if err = found.Validate(query); err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown}, err
	}
	if found.Status == "absent" {
		return ResourceReadResult{Status: state.ResourceAbsent}, nil
	}
	_, req, err := sshDesired(n, query.User, found.Current.Handle.ID)
	if err != nil {
		return ResourceReadResult{}, err
	}
	req.ExpectedRevision = found.Current.Handle.Revision - 1
	digest, err := req.Digest()
	if err != nil || digest != found.Current.Handle.Digest || identity.ActionID != found.Current.Handle.ID {
		return ResourceReadResult{Status: state.ResourceUnknown}, fmt.Errorf("SSH configuration exists without this action's committed identity")
	}
	resource, err := sshResource(n, identity, ref, *found.Current, req.Config)
	return ResourceReadResult{Status: state.ResourcePresent, Resource: resource}, err
}

func (h SSHAccessResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	if pc == nil || pc.exec == nil || n == nil {
		return state.Resource{}, fmt.Errorf("%w: managed SSH context required", ErrConvergenceUnsupported)
	}
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || identity.Validate() != nil || identity.TopologyUID != pc.State().TopologyUID {
		return state.Resource{}, fmt.Errorf("SSH creation requires durable action identity")
	}
	ref, _, err := sshDesired(n, "validation", identity.ActionID)
	if err != nil {
		return state.Resource{}, err
	}
	nodeCtx, connection, query, err := sshNodeConnection(ctx, pc.State(), ref, n.Address.Name, pc.exec.providers)
	if err != nil {
		return state.Resource{}, err
	}
	found, err := connection.DiscoverSSHAccess(nodeCtx, query)
	if err != nil {
		return state.Resource{}, err
	}
	if err = found.Validate(query); err != nil {
		return state.Resource{}, err
	}
	if found.Status != "absent" {
		return state.Resource{}, fmt.Errorf("SSH access already present; reconcile its exact revision before replacement")
	}
	_, req, err := sshDesired(n, query.User, identity.ActionID)
	if err != nil {
		return state.Resource{}, err
	}
	if found.Current != nil {
		req.ExpectedRevision = found.Current.Handle.Revision
	}
	result, err := connection.MutateSSHAccess(nodeCtx, req)
	if err != nil {
		return state.Resource{}, err
	}
	if err = validateSSHMutationResult(query, req, result); err != nil {
		return state.Resource{}, err
	}
	return sshResource(n, identity, ref, result, req.Config)
}

func (h SSHAccessResourceHandler) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	if pc == nil || pc.exec == nil {
		return fmt.Errorf("%w: managed SSH context required", ErrConvergenceUnsupported)
	}
	saved, err := decodeSSHState(current, pc.State())
	if err != nil {
		return err
	}
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || identity.Validate() != nil || identity.OwnerUID != current.OwnerUID || identity.ResourceUID != current.ResourceUID || identity.Generation != current.Generation {
		return fmt.Errorf("SSH delete requires exact resource action identity")
	}
	nodeCtx, connection, query, err := sshNodeConnection(ctx, pc.State(), saved.Node, saved.Handle.AccessID, pc.exec.providers)
	if err != nil {
		return err
	}
	if err = validateSSHStateTarget(saved, query); err != nil {
		return err
	}
	observed, err := connection.InspectSSHAccess(nodeCtx, saved.Handle)
	if err != nil {
		return err
	}
	if observed.Status == "absent" && observed.Revision >= saved.Handle.Revision {
		return nil
	}
	if observed.Status != "present" || observed.Revision != saved.Handle.Revision {
		return fmt.Errorf("SSH access changed since its persisted revision; refusing stale delete")
	}
	req := substrate.SSHAccessMutation{ID: identity.ActionID, ExpectedRevision: saved.Handle.Revision, Delete: true, Config: substrate.SSHAccessConfig{User: query.User, AccessID: query.AccessID}}
	result, err := connection.MutateSSHAccess(nodeCtx, req)
	if err != nil {
		return err
	}
	return validateSSHMutationResult(query, req, result)
}
func (SSHAccessResourceHandler) ExternalID(current state.Resource) string { return current.ExternalID }
func (SSHAccessResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext, providers *driver.Registry) (any, []address.Address, error) {
	cfg := &config.SSHAccessConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	ref, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
	if err != nil {
		return nil, nil, err
	}
	return cfg, []address.Address{ref}, nil
}
