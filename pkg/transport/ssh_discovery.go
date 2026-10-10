package transport

import (
	"context"

	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

func discoverSSHAccess(ctx context.Context, conn taskCaller, identity executionIdentity, query substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	unknown := substrate.SSHAccessDiscovery{Query: query, Status: "unknown"}
	if err := query.Validate(); err != nil {
		return unknown, err
	}
	if identity.nodeID == "" || query.NodeID != identity.nodeID || query.Generation != identity.generation {
		return unknown, guesttask.ErrConflict
	}
	guest := query
	guest.NodeID = ""
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "ssh-discover", SSHQuery: &guest})
	if err != nil {
		return unknown, err
	}
	if response.Version != guesttask.Version || response.Error != "" || response.SSHDiscovery == nil {
		return unknown, guesttask.ErrUnknown
	}
	found := *response.SSHDiscovery
	if err := found.Validate(guest); err != nil {
		return unknown, guesttask.ErrConflict
	}
	if found.Current != nil {
		current := *found.Current
		current.Handle.NodeID = query.NodeID
		found.Current = &current
	}
	found.Query = query
	return found, nil
}

func (c *SSHConnection) DiscoverSSHAccess(ctx context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	return discoverSSHAccess(ctx, c, c.executionIdentity, q)
}
func (c *VsockConnection) DiscoverSSHAccess(ctx context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	return discoverSSHAccess(ctx, c, c.executionIdentity, q)
}
func (c *TaskClient) DiscoverSSHAccess(ctx context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	return discoverSSHAccess(ctx, c, c.identity, q)
}
