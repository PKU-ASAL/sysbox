package transport

import (
	"context"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

func stopSSHAccess(ctx context.Context, conn taskCaller, identity executionIdentity, req substrate.SSHAccessMutation) (substrate.SSHAccessStop, error) {
	var zero substrate.SSHAccessStop
	auth, ok := ctx.Value(taskAuthorizationKey{}).(taskAuthorization)
	digest, err := req.Digest()
	g := auth.Grant.Grant
	op := "guest_ssh_access_apply"
	if req.Delete {
		op = "guest_ssh_access_delete"
	}
	if !ok || err != nil || auth.ID != req.ID || g.ActionID != req.ID || g.Digest != digest || g.Operation != op || g.Scope.Generation != identity.generation || identity.nodeID == "" || auth.Grant.BootID != g.TargetBootID {
		return zero, guesttask.ErrUnauthorized
	}
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "ssh-stop", SSHMutation: &req, Grant: &auth.Grant})
	if err != nil {
		return zero, err
	}
	if response.Version != guesttask.Version || response.Error != "" || response.SSHStop == nil {
		return zero, guesttask.ErrConflict
	}
	stop := *response.SSHStop
	h := stop.Handle
	if h.NodeID != "" || h.TopologyUID != g.Scope.TopologyUID || h.ResourceUID != g.Scope.ResourceUID || h.Generation != g.Scope.Generation || stop.Validate(g.Epoch, auth.Grant.BootID, req) != nil {
		return zero, guesttask.ErrConflict
	}
	stop.Handle.NodeID = identity.nodeID
	if stop.Result != nil {
		result := *stop.Result
		result.Handle = stop.Handle
		stop.Result = &result
	}
	return stop, nil
}
func (c *SSHConnection) StopSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessStop, error) {
	return stopSSHAccess(ctx, c, c.executionIdentity, r)
}
func (c *VsockConnection) StopSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessStop, error) {
	return stopSSHAccess(ctx, c, c.executionIdentity, r)
}
func (c *TaskClient) StopSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessStop, error) {
	return stopSSHAccess(ctx, c, c.identity, r)
}
