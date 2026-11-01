package controlstore

import (
	"github.com/oslab/sysbox/pkg/controlplane"
)

// Payload names may only add a blocker when the incarnation is missing. They
// never grant authority or rebind a command to a new lifecycle incarnation.
func CommandBlocksTopology(command controlplane.AgentCommand, topology, uid string) bool {
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
