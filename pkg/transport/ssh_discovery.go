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
	if found.Query != guest || found.BootID == "" || (found.Status != "present" && found.Status != "absent") {
		return unknown, guesttask.ErrConflict
	}
	if found.Current == nil {
		if found.Status != "absent" || found.Config != nil {
			return unknown, guesttask.ErrConflict
		}
	} else {
		if found.Config == nil {
			return unknown, guesttask.ErrConflict
		}
		h := found.Current.Handle
		if h.NodeID != "" || h.TopologyUID != query.TopologyUID || h.ResourceUID != query.ResourceUID || h.Generation != query.Generation ||
			h.User != query.User || h.AccessID != query.AccessID || h.Revision == 0 || h.Epoch == 0 || h.BootID == "" || (found.Status == "present") != found.Current.Present {
			return unknown, guesttask.ErrConflict
		}
		// Rebuild the exact committed request, including tombstones, to verify
		// the returned public configuration against the immutable handle digest.
		req := substrate.SSHAccessMutation{ID: h.ID, ExpectedRevision: h.Revision - 1, Delete: !found.Current.Present, Config: *found.Config}
		digest, err := req.Digest()
		if err != nil || digest != h.Digest || req.Config.User != h.User || req.Config.AccessID != h.AccessID {
			return unknown, guesttask.ErrConflict
		}
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
