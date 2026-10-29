package persistence

import "github.com/oslab/sysbox/pkg/controlplane"

// The power supervisor signs a stop only after the provider acknowledged the
// action and an exact resource observation confirmed the requested power state.
func CompletePowerFromHost(current controlplane.NodeOperation, record *CommandRecord, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (controlplane.NodeOperation, error) {
	// The authority repository verifies the original grant attempt. A delivery
	// lease may already have advanced after losing the host reply.
	g := receipt.Grant
	if record == nil || current.ID != id || current.AgentID != agent || current.CommandID != record.Command.ID || (current.Operation != "pause" && current.Operation != "resume") || receipt.StoppedAt.IsZero() || receipt.ReceiptID == "" {
		return current, ErrConflict
	}
	cmd := record.Command
	if record.Retired {
		cmd.Operation = current
	}
	req, err := controlplane.FromNodeOperationCommand(cmd)
	if err != nil {
		return current, ErrConflict
	}
	digest, err := req.Digest()
	if err != nil {
		return current, err
	}
	if cmd.Operation.ID != id || cmd.Operation.Operation != current.Operation || cmd.Operation.Topology != current.Topology || cmd.Operation.Node != current.Node || cmd.AgentID != agent || g.AgentID != agent || g.CommandID != cmd.ID || g.ActionID != id || cmd.ActionID != id || g.Operation != "node_power" || g.Digest != digest || g.Scope.TopologyUID != cmd.TopologyUID || g.Scope.ResourceUID != current.ResourceUID || g.Scope.ResourceUID != cmd.ResourceUID || g.Scope.Generation != current.Generation || g.Scope.Generation != cmd.Generation || current.ActionID != id {
		return current, ErrConflict
	}
	next := current
	next.Status, next.Err, next.EndedAt = controlplane.NodeOperationStatusDone, "", receipt.StoppedAt
	if err := ValidateNodeOperationTransition(current, next); err != nil {
		return current, err
	}
	return next, nil
}
