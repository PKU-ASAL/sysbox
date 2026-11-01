package transport

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

func controlTask(ctx context.Context, conn taskCaller, identity executionIdentity, h substrate.ExecutionHandle, control substrate.TerminalControl) error {
	if err := control.Validate(); err != nil {
		return err
	}
	auth, ok := ctx.Value(taskAuthorizationKey{}).(taskAuthorization)
	if !ok || auth.ID != h.ID || h.NodeID != identity.nodeID || h.Generation != identity.generation || h.ID == "" || h.BootID == "" {
		return guesttask.ErrUnauthorized
	}
	g := auth.Grant
	if g.BootID != h.BootID || g.Grant.ActionID != h.ID || g.Grant.Digest != h.Digest || g.Grant.Epoch != h.Epoch || g.Grant.Scope != (controlplane.ExecutionScope{TopologyUID: h.TopologyUID, ResourceUID: h.ResourceUID, Generation: h.Generation}) {
		return guesttask.ErrUnauthorized
	}
	_, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "terminal-control", ID: h.ID, Digest: h.Digest, Grant: &g, Control: &control})
	return err
}

func (c *TaskClient) ControlExecution(ctx context.Context, h substrate.ExecutionHandle, control substrate.TerminalControl) error {
	return controlTask(ctx, c, c.identity, h, control)
}
func (c *SSHConnection) ControlExecution(ctx context.Context, h substrate.ExecutionHandle, control substrate.TerminalControl) error {
	return controlTask(ctx, c, c.executionIdentity, h, control)
}
func (c *VsockConnection) ControlExecution(ctx context.Context, h substrate.ExecutionHandle, control substrate.TerminalControl) error {
	return controlTask(ctx, c, c.executionIdentity, h, control)
}
