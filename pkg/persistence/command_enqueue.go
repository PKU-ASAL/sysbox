package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// Enqueue inserts a new immutable delivery or returns its current live record.
// It never regresses status or changes lease ownership on a producer retry.
func (r *CommandRepository) Enqueue(ctx context.Context, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	cmd = DurableCommand(cmd)
	if validateCommand(cmd) != nil || cmd.Status != controlplane.AgentCommandStatusQueued || cmd.Attempt != 0 || cmd.LeaseOwner != "" || !cmd.LeaseUntil.IsZero() {
		return nil, ErrInvalidRecord
	}
	transition := func(current *CommandRecord) (*CommandRecord, error) {
		if current == nil {
			return &CommandRecord{Command: cmd}, nil
		}
		if current.Retired {
			return nil, ErrConflict
		}
		if validateCommand(current.Command) != nil {
			return nil, ErrCorruptRecord
		}
		a, err := json.Marshal(commandPayload(current.Command))
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(commandPayload(cmd))
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(a, b) {
			return nil, ErrConflict
		}
		return nil, nil
	}
	record, err := r.storage.UpdateCommand(ctx, cmd.ID, transition)
	if errors.Is(err, ErrConflict) {
		// Some SQL adapters cannot row-lock absence. After losing a first
		// insert race, re-read and compare in a new transaction. Changed or
		// retired records still fail through the exact same transition.
		record, err = r.storage.UpdateCommand(ctx, cmd.ID, transition)
	}
	if err != nil {
		return nil, err
	}
	out := record.Command
	return &out, nil
}
