package transport

import (
	"context"
	"math"

	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

type sshAccessMutationHandlerKey struct{}

// SSHAccessMutationHandler is installed by the trusted Run host to journal and
// authorize a child mutation before the provider transmits it.
type SSHAccessMutationHandler func(context.Context, substrate.SSHAccessMutation, substrate.OperationIdentity) (substrate.SSHAccessResult, error)

func WithSSHAccessMutationHandler(ctx context.Context, handler SSHAccessMutationHandler) context.Context {
	return context.WithValue(ctx, sshAccessMutationHandlerKey{}, handler)
}

func mutateSSHAccess(ctx context.Context, conn taskCaller, identity executionIdentity, req substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	var empty substrate.SSHAccessResult
	digest, err := req.Digest()
	if err != nil {
		return empty, err
	}
	auth, ok := ctx.Value(taskAuthorizationKey{}).(taskAuthorization)
	if !ok {
		if handler, exists := ctx.Value(sshAccessMutationHandlerKey{}).(SSHAccessMutationHandler); exists {
			op, bound := substrate.OperationIdentityFromContext(ctx)
			if handler == nil || !bound || op.Validate() != nil || op.ActionID != req.ID || identity.nodeID == "" || identity.generation != op.Generation {
				return empty, guesttask.ErrUnauthorized
			}
			return handler(ctx, req, op)
		}
	}
	g := auth.Grant.Grant
	operation := "guest_ssh_access_apply"
	if req.Delete {
		operation = "guest_ssh_access_delete"
	}
	if !ok || auth.ID != req.ID || g.ActionID != req.ID || g.Operation != operation || g.Digest != digest ||
		identity.nodeID == "" || identity.generation < 1 || identity.generation != g.Scope.Generation ||
		g.Scope.TopologyUID == "" || g.Scope.ResourceUID == "" || g.Epoch == 0 || auth.Grant.BootID == "" ||
		g.TargetBootID != auth.Grant.BootID || req.ExpectedRevision == math.MaxUint64 {
		return empty, guesttask.ErrUnauthorized
	}
	expected := substrate.SSHAccessHandle{ID: req.ID, Digest: digest, User: req.Config.User, AccessID: req.Config.AccessID,
		Revision: req.ExpectedRevision + 1, BootID: auth.Grant.BootID, Epoch: g.Epoch,
		TopologyUID: g.Scope.TopologyUID, ResourceUID: g.Scope.ResourceUID, Generation: g.Scope.Generation}
	// The identity is known before dispatch, even if the outcome is not. Keep
	// it on errors so the caller can observe the original operation after a
	// lost reply. Present is meaningful only when the returned error is nil.
	unknown := substrate.SSHAccessResult{Handle: expected}
	unknown.Handle.NodeID = identity.nodeID
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "ssh-access", SSHMutation: &req, Grant: &auth.Grant})
	if err != nil {
		return unknown, err
	}
	if response.Version != guesttask.Version || response.Error != "" || response.SSHResult == nil || response.SSHResult.Handle != expected || response.SSHResult.Present == req.Delete {
		return unknown, guesttask.ErrConflict
	}
	result := *response.SSHResult
	result.Handle.NodeID = identity.nodeID
	return result, nil
}

func sshAccessGuestHandle(identity executionIdentity, h substrate.SSHAccessHandle) (substrate.SSHAccessHandle, error) {
	if h.NodeID != identity.nodeID || identity.nodeID == "" || h.Generation != identity.generation || h.Generation < 1 ||
		h.ID == "" || h.Digest == "" || h.BootID == "" || h.Epoch == 0 || h.Revision == 0 || h.TopologyUID == "" || h.ResourceUID == "" ||
		h.AccessID == "" || substrate.ValidateSSHAccessUser(h.User) != nil {
		return substrate.SSHAccessHandle{}, guesttask.ErrConflict
	}
	h.NodeID = ""
	return h, nil
}

func observeSSHAccess(ctx context.Context, conn taskCaller, identity executionIdentity, h substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	var empty substrate.SSHAccessResult
	guest, err := sshAccessGuestHandle(identity, h)
	if err != nil {
		return empty, err
	}
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "ssh-observe", SSHHandle: &guest})
	if err != nil {
		return empty, err
	}
	if response.Version != guesttask.Version || response.Error != "" || response.SSHResult == nil || response.SSHResult.Handle != guest {
		return empty, guesttask.ErrConflict
	}
	result := *response.SSHResult
	result.Handle = h
	return result, nil
}

func inspectSSHAccess(ctx context.Context, conn taskCaller, identity executionIdentity, h substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	unknown := substrate.SSHAccessInspection{Status: "unknown"}
	guest, err := sshAccessGuestHandle(identity, h)
	if err != nil {
		return unknown, err
	}
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "ssh-inspect", SSHHandle: &guest})
	if err != nil {
		return unknown, err
	}
	if response.Version != guesttask.Version || response.Error != "" || response.SSHHandle == nil || *response.SSHHandle != guest || response.SSHInspection == nil {
		return unknown, guesttask.ErrConflict
	}
	result := *response.SSHInspection
	if result.Revision < h.Revision || (result.Status != "present" && result.Status != "absent" && result.Status != "drifted") ||
		(result.Status == "present" && result.Revision != h.Revision) || (result.Status == "drifted" && result.Revision == h.Revision) {
		return unknown, guesttask.ErrConflict
	}
	return result, nil
}

func (c *SSHConnection) MutateSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	return mutateSSHAccess(ctx, c, c.executionIdentity, r)
}
func (c *VsockConnection) MutateSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	return mutateSSHAccess(ctx, c, c.executionIdentity, r)
}
func (c *TaskClient) MutateSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	return mutateSSHAccess(ctx, c, c.identity, r)
}
func (c *SSHConnection) ObserveSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	return observeSSHAccess(ctx, c, c.executionIdentity, h)
}
func (c *VsockConnection) ObserveSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	return observeSSHAccess(ctx, c, c.executionIdentity, h)
}
func (c *TaskClient) ObserveSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	return observeSSHAccess(ctx, c, c.identity, h)
}
func (c *SSHConnection) InspectSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	return inspectSSHAccess(ctx, c, c.executionIdentity, h)
}
func (c *VsockConnection) InspectSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	return inspectSSHAccess(ctx, c, c.executionIdentity, h)
}
func (c *TaskClient) InspectSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	return inspectSSHAccess(ctx, c, c.identity, h)
}

var _ substrate.ConnectionSSHAccess = (*SSHConnection)(nil)
var _ substrate.ConnectionSSHAccess = (*VsockConnection)(nil)
var _ substrate.ConnectionSSHAccess = (*TaskClient)(nil)
