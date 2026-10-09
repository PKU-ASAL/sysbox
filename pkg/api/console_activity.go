package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *WorkspaceService) checkConsoleActivity(ctx context.Context, topology string) error {
	if s.loadConsoleSessions == nil {
		return fmt.Errorf("%w: console activity registry unavailable", persistence.ErrConflict)
	}
	sessions, err := s.loadConsoleSessions(ctx, "")
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		if sess.Topology == topology && persistence.ConsoleSessionUnresolved(sess) {
			return fmt.Errorf("%w: unresolved console session prevents maintenance or metadata deletion", persistence.ErrConflict)
		}
	}
	return nil
}
