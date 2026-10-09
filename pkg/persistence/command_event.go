package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// ApplyAgentCommandEvent validates the proof and advances or retires the
// command under the same storage lock. No speculative command is returned.
func (r *CommandRepository) ApplyAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) (*controlplane.AgentCommand, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !topologyName.MatchString(event.CommandID) || !topologyName.MatchString(event.AgentID) {
		return nil, ErrInvalidRecord
	}
	record, err := r.storage.UpdateCommand(ctx, event.CommandID, func(current *CommandRecord) (*CommandRecord, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		cmd := current.Command
		if current.Retired {
			// Only exact console completion evidence can be replayed to repair
			// a failed session projection. Never reopen delivery or accept a new
			// result after retirement; other command protocols keep their rules.
			if cmd.Type != "session_open" || cmd.Session == nil || cmd.Session.ID == "" || !cmd.IsTerminal() || cmd.EndedAt.IsZero() {
				return nil, ErrNotFound
			}
			if event.AgentID != cmd.AgentID || event.Type != "" && event.Type != cmd.Type || event.Status != cmd.Status || event.Error != cmd.Err || event.LeaseOwner != cmd.LeaseOwner || event.Attempt != cmd.Attempt || event.ActionID != cmd.ActionID || event.ResourceUID != cmd.ResourceUID || event.Generation != cmd.Generation || event.LeaseEpoch != cmd.LeaseEpoch || event.Recovery != nil {
				return nil, ErrConflict
			}
			return nil, nil
		}
		if cmd.Type == controlplane.RunRecoveryCommand {
			if !cmd.LeaseUntil.After(r.now()) {
				return nil, ErrConflict
			}
			if event.Recovery != nil && event.Status != controlplane.AgentCommandStatusCompleted {
				return nil, ErrConflict
			}
			if event.LeaseOwner == "" || event.LeaseOwner != cmd.LeaseOwner || event.Attempt < 1 || event.Attempt != cmd.Attempt {
				return nil, ErrConflict
			}
			if event.Status == controlplane.AgentCommandStatusCompleted {
				if err := validateRecoveryReport(cmd, event.Recovery); err != nil {
					return nil, err
				}
			}
		} else if event.Recovery != nil {
			return nil, ErrConflict
		}
		if cmd.AgentID != event.AgentID || event.Type != "" && event.Type != cmd.Type {
			return nil, ErrConflict
		}
		fenced := cmd.ActionID != "" || cmd.ResourceUID != "" || cmd.Generation != 0 || cmd.LeaseEpoch != 0
		if fenced && (event.ActionID != cmd.ActionID || event.ResourceUID != cmd.ResourceUID || event.Generation != cmd.Generation || event.LeaseEpoch != cmd.LeaseEpoch || event.LeaseOwner != cmd.LeaseOwner || event.Attempt != cmd.Attempt) {
			return nil, ErrConflict
		}
		if event.LeaseOwner != "" && event.LeaseOwner != cmd.LeaseOwner || event.Attempt != 0 && event.Attempt != cmd.Attempt || event.ActionID != "" && event.ActionID != cmd.ActionID || event.ResourceUID != "" && event.ResourceUID != cmd.ResourceUID || event.Generation != 0 && event.Generation != cmd.Generation || event.LeaseEpoch != 0 && event.LeaseEpoch != cmd.LeaseEpoch {
			return nil, ErrConflict
		}
		next := event.Status
		switch next {
		case "ack":
			next = controlplane.AgentCommandStatusAcknowledged
		case "started":
			next = controlplane.AgentCommandStatusRunning
		case controlplane.AgentCommandStatusCompleted, controlplane.AgentCommandStatusFailed, controlplane.AgentCommandStatusDenied, controlplane.AgentCommandStatusCancelled:
		default:
			return nil, ErrInvalidRecord
		}
		if cmd.IsTerminal() || !controlplane.CanAdvanceAgentCommandStatus(cmd.Status, next) {
			return nil, ErrConflict
		}
		now := r.now()
		cmd.Status = next
		if next == controlplane.AgentCommandStatusAcknowledged {
			cmd.AckedAt = now
		}
		if cmd.IsTerminal() {
			cmd.EndedAt = now
			cmd.Err = event.Error
		}
		return &CommandRecord{Command: cmd, Retired: cmd.IsTerminal(), Recovery: event.Recovery}, nil
	})
	if err != nil {
		return nil, err
	}
	cmd := record.Command
	return &cmd, nil
}
