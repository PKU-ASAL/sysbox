package persistence

import (
	"reflect"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// Called by the existing three adapters while command and operation share the
// completion transaction, after host signature/authority authentication.
func completeGuestSSHAccessFromHost(current controlplane.NodeOperation, record *CommandRecord, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (controlplane.NodeOperation, error) {
	if record == nil || current.SSHMutation == nil || current.SSHAccess != nil || current.Adoption != nil || current.ID != id || current.ActionID != id || current.AgentID != agent || current.CommandID == "" {
		return current, ErrConflict
	}
	cmd, g := record.Command, receipt.Grant
	if cmd.ID != current.CommandID || cmd.Type != "node_operation" || cmd.AgentID != agent || cmd.ActionID != id ||
		cmd.ResourceUID != current.ResourceUID || cmd.Generation != current.Generation || cmd.Attempt < 1 || uint64(cmd.Attempt) != g.Attempt ||
		g.CommandID != cmd.ID || g.AgentID != agent || g.ActionID != id || g.Operation != current.Operation ||
		g.Scope.TopologyUID != cmd.TopologyUID || g.Scope.ResourceUID != current.ResourceUID || g.Scope.Generation != current.Generation {
		return current, ErrConflict
	}
	if !record.Retired && (cmd.Operation.ID != current.ID || cmd.Operation.Topology != current.Topology || cmd.Operation.Node != current.Node || cmd.Operation.Operation != current.Operation ||
		!reflect.DeepEqual(cmd.Operation.SSHMutation, current.SSHMutation)) {
		return current, ErrConflict
	}
	if err := controlplane.ValidateGuestSSHCompletion(receipt, *current.SSHMutation, current.Node); err != nil {
		return current, ErrConflict
	}
	next := current
	result := *receipt.SSHResult
	next.SSHResult = &result
	next.Status, next.Err, next.EndedAt = controlplane.NodeOperationStatusDone, "", receipt.StoppedAt
	if err := validateNodeOperationTransition(current, next, true); err != nil {
		return current, err
	}
	return next, nil
}
