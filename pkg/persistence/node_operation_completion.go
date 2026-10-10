package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"time"
)

// Called only while the adapter holds both the command and operation write
// boundary. Lease acquisition and completion must not race across this check.
func CompleteNodeOperation(current controlplane.NodeOperation, command *CommandRecord, agent, id string, report controlplane.NodeOperation, now time.Time) (controlplane.NodeOperation, error) {
	if current.Operation == "adoption_apply" || current.Operation == "ssh_access_apply" || current.Operation == "ssh_access_delete" || controlplane.IsGuestSSHAccessOperation(current.Operation) {
		return current, ErrConflict // Only authenticated host evidence can finish these effects.
	}
	if report.ID != id || current.ID != id || agent != current.AgentID || report.AgentID != agent || current.CommandID == "" {
		return current, ErrConflict
	}
	if report.Status != controlplane.NodeOperationStatusDone && report.Status != controlplane.NodeOperationStatusFailed {
		return current, ErrInvalidRecord
	}
	if report.LeaseOwner == "" || report.Attempt < 1 || report.LeaseEpoch < 1 {
		return current, ErrConflict
	}
	report.CommandID = current.CommandID
	if report.EndedAt.IsZero() {
		if current.Status == report.Status {
			report.EndedAt = current.EndedAt
		} else {
			report.EndedAt = now
		}
	}
	if current.Status == controlplane.NodeOperationStatusDone || current.Status == controlplane.NodeOperationStatusFailed {
		if SameNodeOperation(current, report) {
			return current, nil
		}
		return current, ErrConflict
	}
	if command == nil || command.Retired {
		return current, ErrConflict
	}
	cmd := command.Command
	if cmd.ID != current.CommandID || cmd.Type != "node_operation" || cmd.Operation.ID != current.ID || cmd.AgentID != agent || cmd.IsTerminal() || cmd.LeaseOwner == "" || cmd.Attempt < 1 || !cmd.LeaseUntil.After(now) || cmd.LeaseOwner != report.LeaseOwner || cmd.Attempt != report.Attempt || cmd.LeaseEpoch != report.LeaseEpoch || cmd.ActionID != report.ActionID || cmd.ResourceUID != report.ResourceUID || cmd.Generation != report.Generation {
		return current, ErrConflict
	}
	// Mutable lease fields come from the locked command, never the initial
	// operation seed. All other immutable fields and audit history stay guarded.
	validated := current
	validated.LeaseOwner = report.LeaseOwner
	validated.Attempt = report.Attempt
	validated.LeaseEpoch = report.LeaseEpoch
	if err := ValidateNodeOperationTransition(validated, report); err != nil {
		return current, err
	}
	return report, nil
}

func PreserveNodeOperationDispatch(old, next []byte) ([]byte, error) {
	return preserveGuestDispatch(old, next)
}

// CompleteUnderCommandLock requires its caller to hold the shared command
// lock; the record adapter supplies the operation lock inside that boundary.
func (r *NodeOperationRepository) CompleteUnderCommandLock(ctx context.Context, agent, id string, report controlplane.NodeOperation, command *CommandRecord) (*controlplane.NodeOperation, error) {
	return r.CompleteWithTransition(ctx, id, func(current controlplane.NodeOperation) (controlplane.NodeOperation, error) {
		return CompleteNodeOperation(current, command, agent, id, report, time.Now().UTC())
	})
}

// CompleteWithTransition supplies the shared physical write boundary to
// authenticated worker and host completion rules without duplicating storage.
func (r *NodeOperationRepository) CompleteWithTransition(ctx context.Context, id string, transition func(controlplane.NodeOperation) (controlplane.NodeOperation, error)) (*controlplane.NodeOperation, error) {
	if !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	var completed controlplane.NodeOperation
	err := r.records.update(ctx, nodeOperationKind, id, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, ErrNotFound
		}
		current, err := DecodeNodeOperation(raw)
		if err != nil {
			return nil, err
		}
		completed, err = transition(*current)
		if err != nil {
			return nil, err
		}
		next, err := EncodeNodeOperation(completed)
		if err != nil {
			return nil, err
		}
		return preserveGuestDispatch(raw, next)
	})
	if err != nil {
		return nil, err
	}
	return &completed, nil
}
