package persistence

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const GuestExecutionCancelOperation = "guest_execution_cancel"

// Cancellation identity excludes status/version and private request contents.
// The original command ID and creation instant bind the execution incarnation.
func GuestExecutionCancellationCommand(x controlplane.GuestExecution) (controlplane.AgentCommand, error) {
	if !topologyName.MatchString(x.ID) || !topologyName.MatchString(x.AgentID) || !topologyName.MatchString(x.Topology) || !topologyName.MatchString(x.Node) || x.CreatedAt.IsZero() {
		return controlplane.AgentCommand{}, ErrInvalidRecord
	}
	target := controlplane.NodeOperation{ID: x.CommandID, ExternalID: x.ID, Topology: x.Topology, Node: x.Node, AgentID: x.AgentID, CreatedAt: x.CreatedAt.UTC(), Operation: GuestExecutionCancelOperation}
	raw, err := json.Marshal(target)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	return controlplane.AgentCommand{ID: fmt.Sprintf("guest-cancel-%x", sha256.Sum256(raw)), AgentID: x.AgentID, Type: "cancel_command", Operation: target}, nil
}
