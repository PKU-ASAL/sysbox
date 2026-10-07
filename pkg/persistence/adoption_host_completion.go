package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
)

// CompleteAdoptionFromHost consumes host evidence already authenticated and
// matched against the persisted authority by the API. The adapter holds the
// command and operation transaction. Worker expiry/retirement is not authority
// to discard an independently confirmed result, nor to substitute new leases.
func CompleteAdoptionFromHost(current controlplane.NodeOperation, command *CommandRecord, agent, id string, r controlplane.SignedExecutionStopReceipt) (controlplane.NodeOperation, error) {
	g := r.Grant
	if command == nil || current.ID != id || current.AgentID != agent || current.CommandID != command.Command.ID || current.Operation != "adoption_apply" || current.Adoption == nil || r.StoppedAt.IsZero() || r.ReceiptID == "" || r.EvidenceDigest == "" {
		return current, ErrConflict
	}
	cmd := command.Command
	// Retirement intentionally redacts command payload. The operation keeps
	// the immutable adoption request; the receipt digest must match it and
	// the tombstone's retained command/scope identifiers below.
	if command.Retired {
		cmd.Operation = current
	}
	req, err := controlplane.FromAdoptionCommand(cmd)
	if err != nil {
		return current, ErrConflict
	}
	digest, err := req.Digest()
	if err != nil {
		return current, err
	}
	storedDigest, err := current.Adoption.Digest()
	if err != nil || storedDigest != digest {
		return current, ErrConflict
	}
	if g.AgentID != agent || cmd.AgentID != agent || g.CommandID != cmd.ID || g.ActionID != id || g.Operation != "adoption_apply" || g.Digest != digest || g.Scope.TopologyUID != cmd.TopologyUID || g.Scope.ResourceUID != cmd.ResourceUID || g.Scope.Generation != cmd.Generation || current.ActionID != g.ActionID || current.ResourceUID != g.Scope.ResourceUID || current.Generation != g.Scope.Generation || current.Topology != req.Topology {
		return current, ErrConflict
	}
	next := current
	next.Status = controlplane.NodeOperationStatusDone
	next.EndedAt = r.StoppedAt
	next.Err = ""
	if err := ValidateNodeOperationTransition(current, next); err != nil {
		return current, err
	}
	return next, nil
}
