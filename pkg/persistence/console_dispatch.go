package persistence

import (
	"context"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func EncodeConsoleDispatch(sess controlplane.ConsoleSession, cmd controlplane.AgentCommand) ([]byte, error) {
	cmd = DurableCommand(cmd)
	if sess.Status != controlplane.ConsoleSessionStatusQueued || cmd.Type != "session_open" || cmd.Session == nil || cmd.AgentID != sess.AgentID || !SameConsoleSession(sess, *cmd.Session) || sess.Node == "" || sess.CreatedAt.IsZero() {
		return nil, ErrInvalidRecord
	}
	raw, err := encodeGuestDispatch(cmd.ID, cmd, func() ([]byte, error) { return EncodeConsoleSession(sess) })
	if err != nil {
		return nil, err
	}
	command, err := EncodeCommandRecord(CommandRecord{Command: cmd})
	if err != nil {
		return nil, err
	}
	return withGuestDispatch(raw, command)
}

// Admit publishes a new session and its first outbox record as one storage
// mutation. Reusing an existing session is a conflict, never a fresh dispatch.
func (r *ConsoleRepository) Admit(ctx context.Context, sess controlplane.ConsoleSession, cmd controlplane.AgentCommand) error {
	if _, err := EncodeConsoleDispatch(sess, cmd); err != nil {
		return err
	}
	cmd = DurableCommand(cmd)
	return r.storage.UpdateConsoleSession(ctx, sess.ID, &cmd, func(current *controlplane.ConsoleSession) (*controlplane.ConsoleSession, error) {
		if current != nil {
			return nil, ErrConflict
		}
		return &sess, nil
	})
}

// ConsoleDispatchSeed verifies the immutable birth record against the current
// session. Mutable command records/tombstones supersede this filesystem seed.
func ConsoleDispatchSeed(raw []byte) (*CommandRecord, error) {
	seed, err := guestDispatchSeed(raw)
	if err != nil || seed == nil {
		return seed, err
	}
	var sess controlplane.ConsoleSession
	if json.Unmarshal(raw, &sess) != nil || seed.Command.Session == nil || ValidateConsoleSessionCAS(*seed.Command.Session, sess) != nil {
		return nil, ErrCorruptRecord
	}
	if _, err := EncodeConsoleDispatch(*seed.Command.Session, seed.Command); err != nil || seed.Retired {
		return nil, ErrCorruptRecord
	}
	return seed, nil
}

func PreserveConsoleDispatch(previous []byte, next controlplane.ConsoleSession) ([]byte, error) {
	raw, err := EncodeConsoleSession(next)
	if err != nil {
		return nil, err
	}
	seed, err := ConsoleDispatchSeed(previous)
	if err != nil {
		return nil, err
	}
	if seed != nil {
		if ValidateConsoleSessionCAS(*seed.Command.Session, next) != nil {
			return nil, ErrConflict
		}
		command, err := EncodeCommandRecord(*seed)
		if err != nil {
			return nil, err
		}
		raw, err = withGuestDispatch(raw, command)
		if err != nil {
			return nil, err
		}
	}
	cancellation, err := ConsoleCancellationSeed(previous)
	if err != nil || cancellation == nil {
		return raw, err
	}
	return WithConsoleCancellation(raw, cancellation.Command)
}
