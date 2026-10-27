package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func ConsoleCancellationCommand(sess controlplane.ConsoleSession) controlplane.AgentCommand {
	identity := controlplane.ConsoleSession{ID: sess.ID, AgentID: sess.AgentID, Topology: sess.Topology, Node: sess.Node, CreatedAt: sess.CreatedAt}
	raw, _ := json.Marshal(identity)
	id := fmt.Sprintf("console-cancel-%x", sha256.Sum256(raw))
	return controlplane.AgentCommand{ID: id, AgentID: sess.AgentID, Type: "cancel_command", Session: &identity}
}

func (r *ConsoleRepository) Cancel(ctx context.Context, old, next controlplane.ConsoleSession) error {
	if err := ValidateConsoleSessionCAS(old, next); err != nil {
		return err
	}
	if next.Status != controlplane.ConsoleSessionStatusCancelling || !next.EndedAt.IsZero() || next.CreatedAt.IsZero() || !topologyName.MatchString(next.AgentID) || !topologyName.MatchString(next.Node) {
		return ErrInvalidRecord
	}
	cmd := DurableCommand(ConsoleCancellationCommand(next))
	cmd.CreatedAt = time.Now().UTC()
	return r.storage.UpdateConsoleSession(ctx, old.ID, &cmd, func(current *controlplane.ConsoleSession) (*controlplane.ConsoleSession, error) {
		if current == nil || !SameConsoleSession(*current, old) {
			return nil, ErrConflict
		}
		return &next, nil
	})
}

func ConsoleCancellationSeed(raw []byte) (*CommandRecord, error) {
	var record struct {
		controlplane.ConsoleSession
		Cancellation json.RawMessage `json:"cancel_dispatch_command"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, ErrCorruptRecord
	}
	if len(record.Cancellation) == 0 {
		return nil, nil
	}
	seed, err := DecodeCommandRecord(record.Cancellation)
	if err != nil {
		return nil, err
	}
	expected := DurableCommand(ConsoleCancellationCommand(record.ConsoleSession))
	if _, err := PrepareCommandEnqueue(seed, expected); err != nil || seed.Command.Status != controlplane.AgentCommandStatusQueued || seed.Command.Attempt != 0 || seed.Command.LeaseOwner != "" || !seed.Command.LeaseUntil.IsZero() {
		return nil, ErrCorruptRecord
	}
	return seed, nil
}

func WithConsoleCancellation(raw []byte, cmd controlplane.AgentCommand) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := decodeGuestObject(raw, &object); err != nil {
		return nil, err
	}
	command, err := EncodeCommandRecord(CommandRecord{Command: cmd})
	if err != nil {
		return nil, err
	}
	object["cancel_dispatch_command"] = command
	return json.Marshal(object)
}
