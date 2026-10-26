package api

import (
	"errors"
	"reflect"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// The caller reads the current operation inside the authority/command write
// boundary. A retained queued dispatch is not proof its operation is live.
func (b apiExecutionGrantStorage) checkGuestSSHSource(record *persistence.CommandRecord, read func(string) (*controlplane.NodeOperation, error)) error {
	if record != nil && record.Command.Type == "node_operation" && (record.Command.Operation.Operation == "pause" || record.Command.Operation.Operation == "resume") {
		current, err := read(record.Command.Operation.ID)
		if err != nil {
			return err
		}
		if !nodeCommandMatchesOperation(record.Command, current) || (current.Status != controlplane.NodeOperationStatusQueued && current.Status != controlplane.NodeOperationStatusRunning) {
			return persistence.ErrConflict
		}
		return nil
	}
	if record != nil && record.Command.Type == "node_operation" && record.Command.Operation.Operation == "adoption_inspect" {
		cmd := record.Command
		current, err := read(cmd.ActionID)
		if err != nil {
			return err
		}
		if current == nil || current.CommandID != cmd.ID || current.AgentID != cmd.AgentID || current.Inspection == nil || !reflect.DeepEqual(current.Inspection, cmd.Operation.Inspection) || current.InspectionEvidence != nil || (current.Status != controlplane.NodeOperationStatusQueued && current.Status != controlplane.NodeOperationStatusRunning) {
			return persistence.ErrConflict
		}
		return nil
	}
	if record == nil || record.Command.Type != "node_operation" || !controlplane.IsGuestSSHAccessOperation(record.Command.Operation.Operation) || b.sourceErr != nil && *b.sourceErr != nil {
		return nil
	}
	cmd := record.Command
	current, err := read(cmd.Operation.ID)
	if errors.Is(err, persistence.ErrNotFound) {
		err = persistence.ErrConflict
	}
	if err == nil && (current == nil || current.ID != cmd.ActionID || current.ActionID != cmd.ActionID || current.CommandID != cmd.ID ||
		current.AgentID != cmd.AgentID || current.Topology != cmd.Operation.Topology || current.Node != cmd.Operation.Node ||
		current.ResourceUID != cmd.ResourceUID || current.Generation != cmd.Generation || current.Operation != cmd.Operation.Operation ||
		!current.CreatedAt.Equal(cmd.Operation.CreatedAt) || current.SSHResult != nil || current.SSHAccess != nil || current.Adoption != nil ||
		(current.Status != controlplane.NodeOperationStatusQueued && current.Status != controlplane.NodeOperationStatusRunning) ||
		!reflect.DeepEqual(current.SSHMutation, cmd.Operation.SSHMutation)) {
		err = persistence.ErrConflict
	}
	// SSH mutation authorization must abort the enclosing authority transaction
	// on source conflict. Returning nil here would let the surrounding callback
	// continue to fn(raw) after recording sourceErr, which could accidentally
	// issue a grant before the clock-floor fallback runs.
	return err
}

func nodeCommandMatchesOperation(cmd controlplane.AgentCommand, op *controlplane.NodeOperation) bool {
	return op != nil && op.ID == cmd.Operation.ID && op.ID == cmd.ActionID && op.ActionID == cmd.ActionID && op.CommandID == cmd.ID && op.AgentID == cmd.AgentID && op.Topology == cmd.Operation.Topology && op.Node == cmd.Operation.Node && op.Operation == cmd.Operation.Operation && op.ResourceUID == cmd.ResourceUID && op.Generation == cmd.Generation && op.CreatedAt.Equal(cmd.Operation.CreatedAt)
}
