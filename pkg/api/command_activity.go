package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
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
		if commandBlocksTopology(command, topology, st.TopologyUID) {
			return fmt.Errorf("%w: active agent command prevents maintenance or metadata deletion", persistence.ErrConflict)
		}
	}
	return nil
}

// Payload names may only add a blocker when the incarnation is missing. They
// never grant authority or rebind a command to a new lifecycle incarnation.
func commandBlocksTopology(command controlplane.AgentCommand, topology, uid string) bool {
	if command.IsTerminal() {
		return false
	}
	if command.TopologyUID != "" {
		return command.TopologyUID == uid
	}
	return command.Run != nil && command.Run.Topology == topology ||
		command.Operation.Topology == topology ||
		command.Execution != nil && command.Execution.Topology == topology ||
		command.FilePut != nil && command.FilePut.Topology == topology ||
		command.Session != nil && command.Session.Topology == topology
}
