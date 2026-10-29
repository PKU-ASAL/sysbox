package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Called under the same authority lock as cancellation and grant mutation.
func (b apiExecutionGrantStorage) checkConsoleSource(record *persistence.CommandRecord, read func(string) (*controlplane.ConsoleSession, error)) error {
	if b.lifecycle == nil || record == nil || record.Command.Type != "session_open" || b.sourceErr != nil && *b.sourceErr != nil {
		return nil
	}
	c := record.Command
	if c.Session == nil {
		return b.rejectGrantSource(persistence.ErrConflict)
	}
	s, err := read(c.Session.ID)
	if err == nil && (s == nil || persistence.ValidateConsoleSessionCAS(*c.Session, *s) != nil || s.TopologyUID != c.TopologyUID || s.ResourceUID != c.ResourceUID || s.Generation != c.Generation || s.HostCompletion != "" || s.Status != controlplane.ConsoleSessionStatusQueued && s.Status != controlplane.ConsoleSessionStatusRunning) {
		err = persistence.ErrConflict
	}
	return b.rejectGrantSource(err)
}

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
