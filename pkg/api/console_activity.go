package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// lost is a transport observation, even when restart supplied an EndedAt.
// These checks describe registry completion, not proof of remote quiescence;
// outstanding execution authority must still be checked at the commit boundary.
func consoleActivityUnresolved(sess controlplane.ConsoleSession) bool {
	if sess.CreatedAt.IsZero() || sess.EndedAt.IsZero() || sess.EndedAt.Before(sess.CreatedAt) {
		return true
	}
	switch sess.Status {
	case controlplane.ConsoleSessionStatusClosed, controlplane.ConsoleSessionStatusFailed, controlplane.ConsoleSessionStatusCancelled, controlplane.ConsoleSessionStatusDenied:
		return false
	default:
		return true
	}
}

func (s *WorkspaceService) checkConsoleActivity(ctx context.Context, topology string) error {
	if s.loadConsoleSessions == nil {
		return fmt.Errorf("%w: console activity registry unavailable", persistence.ErrConflict)
	}
	sessions, err := s.loadConsoleSessions(ctx, "")
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		if sess.Topology == topology && consoleActivityUnresolved(sess) {
			return fmt.Errorf("%w: unresolved console session prevents maintenance or metadata deletion", persistence.ErrConflict)
		}
	}
	return nil
}
