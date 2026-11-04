package persistence

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// ApplyActionOutcome uses the same CAS/state transition as Repository while
// allowing an owning storage transaction to include command authorization.
func ApplyActionOutcome(raw []byte, action runtime.ActionRecord, patch *runtime.StatePatch) ([]byte, error) {
	if err := validateAction(action); err != nil {
		return nil, err
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, err
	}
	if err := validateAggregate(a, action.Topology); err != nil {
		return nil, err
	}
	if err := applyActionOutcome(a, action, patch); err != nil {
		return nil, err
	}
	if err := validateAggregate(a, action.Topology); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

// ValidateRecoveryCommit must run under the same storage transaction/locks as
// the lifecycle write. It grants no provider effects, only an exact Run's
// existing journal outcome. Source records must remain locked through commit.
func ValidateRecoveryCommit(proof controlplane.AgentCommandEvent, expected controlplane.Run, record *CommandRecord, owner *controlplane.Agent, runs []controlplane.Run, action runtime.ActionRecord, now time.Time) error {
	if record == nil || record.Retired || owner == nil || owner.ID != expected.AgentID || owner.IsBlocked() || !slices.Contains(owner.Capabilities, controlplane.RunRecoveryCapability) {
		return ErrConflict
	}
	cmd := record.Command
	if proof.CommandID == "" || proof.LeaseOwner == "" || proof.Attempt < 1 || cmd.ID != proof.CommandID || cmd.AgentID != expected.AgentID || cmd.Type != controlplane.RunRecoveryCommand || cmd.RecoveryMode != controlplane.RunRecoveryRecover || cmd.IsTerminal() || cmd.LeaseOwner != proof.LeaseOwner || cmd.Attempt != proof.Attempt || !cmd.LeaseUntil.After(now) || cmd.Run == nil {
		return ErrConflict
	}
	var current *controlplane.Run
	for i := range runs {
		run := &runs[i]
		if run.Topology == expected.Topology && run.Status.IsActive() {
			return ErrConflict
		}
		if run.ID == expected.ID {
			current = run
		}
	}
	if current == nil || current.QueuedAt.IsZero() || !controlplane.SameRunIncarnation(*current, expected) || !controlplane.SameRunIncarnation(*current, *cmd.Run) || current.AgentID != expected.AgentID || current.Attempt != expected.Attempt || current.Attempt != cmd.Run.Attempt || current.LeaseOwner != expected.LeaseOwner || current.LeaseOwner != cmd.Run.LeaseOwner || current.Status != controlplane.RunFailed || !current.Recoverable || current.LeaseUntil.After(now) {
		return ErrConflict
	}
	if action.RunID != current.ID || action.Topology != current.Topology || !action.RunQueuedAt.Equal(current.QueuedAt) || action.RunRevision != current.Revision {
		return ErrConflict
	}
	return nil
}
