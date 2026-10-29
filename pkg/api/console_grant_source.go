package api

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) consoleTaskStop(ctx context.Context, cmd controlplane.AgentCommand, g controlplane.ExecutionGrant) (controlplane.GuestTaskStop, error) {
	var zero controlplane.GuestTaskStop
	task, err := guesttask.FromConsoleCommand(cmd)
	if err != nil {
		return zero, err
	}
	digest, err := task.Digest()
	if err != nil {
		return zero, err
	}
	if g.AgentID != cmd.AgentID || g.CommandID != cmd.ID || g.ActionID != task.ID || g.Digest != digest || g.Scope != (controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}) {
		return zero, persistence.ErrConflict
	}
	sess, err := s.apiStore.GetConsoleSession(ctx, task.ID)
	if err != nil {
		return zero, err
	}
	if sess == nil || persistence.ValidateConsoleSessionCAS(*cmd.Session, *sess) != nil {
		return zero, persistence.ErrConflict
	}
	proof, err := controlplane.SignExecutionCancellation(s.grantSigner, g)
	if err != nil {
		return zero, err
	}
	return controlplane.GuestTaskStop{Topology: sess.Topology, Node: sess.Node, Cancellation: proof}, nil
}
