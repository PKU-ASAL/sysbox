package persistence

import (
	"context"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// Guest dispatches keep one authoritative operation record. Filesystem records
// carry the initial command alongside it; SQL commits the command row in the
// same transaction. Mutable command records (including retirement tombstones)
// supersede the initial filesystem seed.
func EncodeGuestExecutionDispatch(x controlplane.GuestExecution, cmd controlplane.AgentCommand) ([]byte, error) {
	cmd = DurableCommand(cmd)
	if executionCodec.validate(x) != nil || x.Version != 1 || x.Status != controlplane.GuestExecutionQueued || x.CommandID != cmd.ID || x.AgentID != cmd.AgentID || cmd.Type != "guest_execution" || cmd.Execution == nil || cmd.Execution.ID != x.ID || cmd.Execution.Topology != x.Topology || cmd.Execution.Node != x.Node || !sameGuestRequest(x.Request, cmd.ExecutionRequest) {
		return nil, ErrInvalidRecord
	}
	return encodeGuestDispatch(x.CommandID, cmd, func() ([]byte, error) { return executionCodec.encode(x) })
}
func EncodeGuestExecutionDispatchRecord(x controlplane.GuestExecution, cmd controlplane.AgentCommand) ([]byte, error) {
	raw, err := EncodeGuestExecutionDispatch(x, cmd)
	if err != nil {
		return nil, err
	}
	command, err := EncodeCommandRecord(CommandRecord{Command: cmd})
	if err != nil {
		return nil, err
	}
	return withGuestDispatch(raw, command)
}
func EncodeGuestFileDispatch(x controlplane.GuestFileOperation, cmd controlplane.AgentCommand) ([]byte, error) {
	cmd = DurableCommand(cmd)
	if fileOperationCodec.validate(x) != nil || x.Version != 1 || x.Status != controlplane.GuestExecutionQueued || x.CommandID != cmd.ID || x.AgentID != cmd.AgentID || cmd.Type != "guest_file_put" || cmd.FilePut == nil || cmd.FilePut.ID != x.ID || cmd.FilePut.Topology != x.Topology || cmd.FilePut.Node != x.Node {
		return nil, ErrInvalidRecord
	}
	return encodeGuestDispatch(x.CommandID, cmd, func() ([]byte, error) { return fileOperationCodec.encode(x) })
}
func EncodeGuestFileDispatchRecord(x controlplane.GuestFileOperation, cmd controlplane.AgentCommand) ([]byte, error) {
	raw, err := EncodeGuestFileDispatch(x, cmd)
	if err != nil {
		return nil, err
	}
	command, err := EncodeCommandRecord(CommandRecord{Command: cmd})
	if err != nil {
		return nil, err
	}
	return withGuestDispatch(raw, command)
}
func encodeGuestDispatch(id string, cmd controlplane.AgentCommand, encode func() ([]byte, error)) ([]byte, error) {
	cmd = DurableCommand(cmd)
	if id == "" || validateCommand(cmd) != nil || cmd.Status != controlplane.AgentCommandStatusQueued || cmd.Attempt != 0 || cmd.LeaseOwner != "" || !cmd.LeaseUntil.IsZero() {
		return nil, ErrInvalidRecord
	}
	return encode()
}

// CreateFilesystemGuestDispatchUnderAuthority requires the caller's shared
// record and authority locks through the aggregate/outbox publication.
func (r *GuestRepository) CreateFilesystemGuestDispatchUnderAuthority(ctx context.Context, execution *controlplane.GuestExecution, file *controlplane.GuestFileOperation, cmd controlplane.AgentCommand) error {
	storage, ok := r.records.(*fileGuestRecords)
	if !ok {
		return ErrInvalidRecord
	}
	var raw []byte
	var err error
	var kind guestKind
	var id string
	switch {
	case execution != nil && file == nil:
		kind = guestExecutionKind
		id = execution.ID
		raw, err = EncodeGuestExecutionDispatch(*execution, cmd)
	case file != nil && execution == nil:
		kind = guestFileKind
		id = file.ID
		raw, err = EncodeGuestFileDispatch(*file, cmd)
	default:
		return ErrInvalidRecord
	}
	if err != nil {
		return err
	}
	command, err := EncodeCommandRecord(CommandRecord{Command: cmd})
	if err != nil {
		return err
	}
	raw, err = withGuestDispatch(raw, command)
	if err != nil {
		return err
	}
	return storage.updateRecord(ctx, kind, id, func(current []byte) ([]byte, error) {
		if current != nil {
			return nil, ErrConflict
		}
		return raw, nil
	})
}

func withGuestDispatch(raw, command []byte) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := decodeGuestObject(raw, &object); err != nil {
		return nil, err
	}
	object["dispatch_command"] = json.RawMessage(command)
	return json.Marshal(object)
}
func guestDispatchSeed(raw []byte) (*CommandRecord, error) {
	var object map[string]json.RawMessage
	if err := decodeGuestObject(raw, &object); err != nil {
		return nil, err
	}
	command, ok := object["dispatch_command"]
	if !ok {
		return nil, nil
	}
	return DecodeCommandRecord(command)
}
func preserveGuestDispatch(old, next []byte) ([]byte, error) {
	if old == nil {
		return next, nil
	}
	var previous map[string]json.RawMessage
	if err := decodeGuestObject(old, &previous); err != nil {
		return nil, err
	}
	if cancellation, ok := previous["cancel_dispatch_command"]; ok {
		if _, err := GuestExecutionCancellationSeed(old); err != nil {
			return nil, err
		}
		var updated map[string]json.RawMessage
		if err := decodeGuestObject(next, &updated); err != nil {
			return nil, err
		}
		updated["cancel_dispatch_command"] = cancellation
		var err error
		next, err = json.Marshal(updated)
		if err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"completion_receipt", "host_completion_receipt", "host_completion"} {
		if receipt, ok := previous[key]; ok {
			var updated map[string]json.RawMessage
			if err := decodeGuestObject(next, &updated); err != nil {
				return nil, err
			}
			updated[key] = receipt
			var err error
			next, err = json.Marshal(updated)
			if err != nil {
				return nil, err
			}
		}
	}
	record, err := guestDispatchSeed(old)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return next, nil
	}
	command, err := EncodeCommandRecord(*record)
	if err != nil {
		return nil, err
	}
	return withGuestDispatch(next, command)
}
func (r *GuestRepository) FilesystemGuestDispatchCommands(ctx context.Context) ([]CommandRecord, error) {
	if _, ok := r.records.(*fileGuestRecords); !ok {
		return nil, ErrInvalidRecord
	}
	var out []CommandRecord
	for _, kind := range []guestKind{guestExecutionKind, guestFileKind, nodeOperationKind} {
		rows, err := r.records.list(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if kind == guestExecutionKind {
				cancellation, err := GuestExecutionCancellationSeed(row.data)
				if err != nil {
					return nil, err
				}
				if cancellation != nil {
					if _, err := decodeGuestRecord(row.id, row.data, executionCodec); err != nil {
						return nil, err
					}
					out = append(out, *cancellation)
				}
			}
			seed, err := guestDispatchSeed(row.data)
			if err != nil {
				return nil, err
			}
			if seed == nil {
				continue
			}
			if kind == guestExecutionKind {
				x, err := decodeGuestRecord(row.id, row.data, executionCodec)
				if err != nil {
					return nil, err
				}
				if x.CommandID != seed.Command.ID || x.AgentID != seed.Command.AgentID || seed.Command.Execution == nil || seed.Command.Execution.ID != x.ID {
					return nil, ErrCorruptRecord
				}
			} else if kind == nodeOperationKind {
				op, err := DecodeNodeOperation(row.data)
				if err != nil {
					return nil, err
				}
				if op.ID != row.id || op.CommandID != seed.Command.ID || op.AgentID != seed.Command.AgentID || seed.Command.Operation.ID != op.ID || seed.Command.Type != "node_operation" || seed.Command.ResourceUID != op.ResourceUID || seed.Command.Generation != op.Generation || seed.Command.ActionID != op.ActionID {
					return nil, ErrCorruptRecord
				}
				birth := seed.Command.Operation
				birth.CommandID = seed.Command.ID
				birth.LeaseOwner, birth.LeaseEpoch, birth.Attempt = op.LeaseOwner, op.LeaseEpoch, op.Attempt
				// This checks an already committed snapshot against its immutable
				// outbox seed, not a worker's proposed completion. Host SSH results
				// legitimately differ from that queued seed. The write boundary
				// still requires authenticated host completion for these fields.
				if validateNodeOperationTransition(birth, *op, true) != nil {
					return nil, ErrCorruptRecord
				}
			} else {
				x, err := decodeGuestRecord(row.id, row.data, fileOperationCodec)
				if err != nil {
					return nil, err
				}
				if x.CommandID != seed.Command.ID || x.AgentID != seed.Command.AgentID || seed.Command.FilePut == nil || seed.Command.FilePut.ID != x.ID {
					return nil, ErrCorruptRecord
				}
			}
			out = append(out, *seed)
		}
	}
	return out, nil
}
