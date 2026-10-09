package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/persistence"
)

// This is a conservative preflight shared by maintenance and deletion. It is
// not a transaction spanning command admission and lifecycle mutation.
func (s *WorkspaceService) checkCommandActivity(ctx context.Context, topology string) error {
	if s.loadAgentCommands == nil || s.stateManager == nil {
		return fmt.Errorf("%w: command activity registry unavailable", persistence.ErrConflict)
	}
	commands, err := s.loadAgentCommands(ctx)
	if err != nil {
		return err
	}
	pending := false
	for _, command := range commands {
		if !command.IsTerminal() {
			pending = true
			break
		}
	}
	if !pending {
		return nil
	}
	mgr, err := s.stateManager(topology)
	if err != nil {
		return err
	}
	st, err := mgr.LoadWithContext(ctx)
	if err != nil {
		return err
	}
	if st == nil || st.TopologyUID == "" {
		return persistence.ErrCorruptRecord
	}
	for _, command := range commands {
		if command.TopologyUID == st.TopologyUID && !command.IsTerminal() {
			return fmt.Errorf("%w: active agent command prevents maintenance or metadata deletion", persistence.ErrConflict)
		}
	}
	return nil
}
