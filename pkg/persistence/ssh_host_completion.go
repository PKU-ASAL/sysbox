package persistence

import "github.com/oslab/sysbox/pkg/controlplane"

// CompleteSSHAccessFromHost consumes authenticated, authority-validated host
// evidence while the adapter holds operation and command in one transaction.
func CompleteSSHAccessFromHost(current controlplane.NodeOperation, record *CommandRecord, agent, id string, receipt controlplane.SignedExecutionStopReceipt) (controlplane.NodeOperation, error) {
	g := receipt.Grant
	if record == nil || current.SSHAccess == nil || current.ID != id || current.AgentID != agent || current.CommandID != record.Command.ID || (current.Operation != "ssh_access_apply" && current.Operation != "ssh_access_delete") || receipt.EvidenceDigest == "" || receipt.StoppedAt.IsZero() || receipt.ReceiptID == "" {
		return current, ErrConflict
	}
	cmd := record.Command
	if record.Retired {
		cmd.Operation = current
	}
	req, err := controlplane.FromSSHAccessCommand(cmd)
	if err != nil {
		return current, ErrConflict
	}
	digest, err := req.Digest()
	if err != nil {
		return current, err
	}
	stored, err := current.SSHAccess.Digest()
	if err != nil || digest != stored || cmd.Operation.Operation != current.Operation || cmd.AgentID != agent || g.AgentID != agent || g.CommandID != cmd.ID || g.ActionID != id || g.Operation != current.Operation || g.Digest != digest || g.Scope.TopologyUID != cmd.TopologyUID || g.Scope.ResourceUID != current.ResourceUID || g.Scope.ResourceUID != cmd.ResourceUID || g.Scope.Generation != current.Generation || g.Scope.Generation != cmd.Generation || current.ActionID != id || current.Topology != req.Topology || current.Node != req.Node {
		return current, ErrConflict
	}
	next := current
	next.Status = controlplane.NodeOperationStatusDone
	next.EndedAt = receipt.StoppedAt
	next.Err = ""
	if err := ValidateNodeOperationTransition(current, next); err != nil {
		return current, err
	}
	return next, nil
}
