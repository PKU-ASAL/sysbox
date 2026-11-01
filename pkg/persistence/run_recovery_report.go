package persistence

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func validateRecoveryReport(cmd controlplane.AgentCommand, r *controlplane.RunRecoveryReport) error {
	if r != nil && cmd.Run != nil && (r.RunAttempt != cmd.Run.Attempt || r.RunLeaseOwner != cmd.Run.LeaseOwner) {
		return ErrConflict
	}
	if r == nil || cmd.Run == nil || r.CommandID != cmd.ID || r.RunID != cmd.Run.ID || r.AgentID != cmd.AgentID || !r.QueuedAt.Equal(cmd.Run.QueuedAt) || r.Revision != cmd.Run.Revision || r.Mode != cmd.RecoveryMode || r.TopologyUID == "" || r.StateSerial < 0 || r.ObservedAt.IsZero() || len(r.Resources) == 0 {
		return ErrConflict
	}
	seen := map[string]bool{}
	for _, resource := range r.Resources {
		if resource.ActionID == "" || seen[resource.ActionID] || resource.Resource == "" || resource.OwnerUID != r.TopologyUID || resource.ResourceUID == "" || resource.Generation < 1 {
			return ErrConflict
		}
		seen[resource.ActionID] = true
		switch resource.JournalStatus {
		case "intent_committed", "succeeded", "uncertain", "blocked":
		default:
			return ErrConflict
		}
		switch resource.Status {
		case "present", "absent", "unknown":
		default:
			return ErrConflict
		}
	}
	return nil
}
func (r *CommandRepository) GetRunRecoveryReport(ctx context.Context, agent, id string) (*controlplane.RunRecoveryReport, error) {
	if !topologyName.MatchString(agent) || !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	record, err := r.storage.ReadCommand(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Command.AgentID != agent {
		return nil, ErrNotFound
	}
	if record.Recovery == nil {
		if record.Command.IsTerminal() {
			return nil, fmt.Errorf("%w: recovery produced no verified report", ErrConflict)
		}
		return nil, ErrNotFound
	}
	if !record.Retired || record.Command.Status != controlplane.AgentCommandStatusCompleted {
		return nil, ErrCorruptRecord
	}
	if record.Command.Type != controlplane.RunRecoveryCommand || validateRecoveryReport(record.Command, record.Recovery) != nil {
		return nil, ErrCorruptRecord
	}
	return record.Recovery, nil
}
