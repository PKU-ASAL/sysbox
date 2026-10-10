package hostsupervisor

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (d *Dispatcher) DiscoverSSHAccess(ctx context.Context, ref TargetRef, accessID string) (substrate.SSHAccessDiscovery, error) {
	var empty substrate.SSHAccessDiscovery
	target, err := d.target(ctx, ref, controlplane.ExecutionGrant{Scope: ref.Scope, AgentID: d.AgentID})
	if err != nil {
		return empty, err
	}
	query := substrate.SSHAccessQuery{NodeID: target.NodeID, User: target.User, AccessID: accessID,
		TopologyUID: target.Scope.TopologyUID, ResourceUID: target.Scope.ResourceUID, Generation: target.Scope.Generation}
	if err := query.Validate(); err != nil {
		return empty, err
	}
	reader, ok := target.Connection.(interface {
		DiscoverSSHAccess(context.Context, substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error)
	})
	if !ok {
		return empty, substrate.ErrNotSupported
	}
	ctx = substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "ssh-discover", TopologyUID: ref.Scope.TopologyUID,
		OwnerUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation})
	found, err := reader.DiscoverSSHAccess(ctx, query)
	if err != nil {
		return empty, err
	}
	if err := found.Validate(query); err != nil {
		return empty, err
	}
	// Provider IDs remain selected by the resolver; the worker sees the
	// canonical node name, just as it does for host-supervised task handles.
	found.Query.NodeID = ref.Node
	if found.Current != nil {
		current := *found.Current
		current.Handle.NodeID = ref.Node
		found.Current = &current
	}
	return found, nil
}

func (c *TaskClient) DiscoverManagedSSHAccess(ctx context.Context, accessID string) (substrate.SSHAccessDiscovery, error) {
	response, err := c.call(ctx, Request{Operation: "ssh-discover", SSHAccessID: accessID})
	if err != nil {
		return substrate.SSHAccessDiscovery{}, err
	}
	if response.SSHDiscovery == nil {
		return substrate.SSHAccessDiscovery{}, fmt.Errorf("host SSH discovery missing")
	}
	found := *response.SSHDiscovery
	query := substrate.SSHAccessQuery{NodeID: c.target.Node, User: found.Query.User, AccessID: accessID,
		TopologyUID: c.target.Scope.TopologyUID, ResourceUID: c.target.Scope.ResourceUID, Generation: c.target.Scope.Generation}
	if err := found.Validate(query); err != nil {
		return substrate.SSHAccessDiscovery{}, err
	}
	return found, nil
}
