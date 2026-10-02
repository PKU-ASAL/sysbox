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
	if executionCodec.validate(x) != nil || x.Version != 1 || x.Status != controlplane.GuestExecutionQueued || x.CommandID != cmd.ID || x.AgentID != cmd.AgentID || cmd.Type != "guest_execution" || cmd.Execution == nil || cmd.Execution.ID != x.ID || cmd.Execution.Topology != x.Topology || cmd.Execution.Node != x.Node || !sameGuestRequest(x.Request, cmd.ExecutionRequest) {
		return nil, ErrInvalidRecord
	}
	return encodeGuestDispatch(x.CommandID, cmd, func() ([]byte, error) { return executionCodec.encode(x) })
}
func EncodeGuestFileDispatch(x controlplane.GuestFileOperation, cmd controlplane.AgentCommand) ([]byte, error) {
	if fileOperationCodec.validate(x) != nil || x.Version != 1 || x.Status != controlplane.GuestExecutionQueued || x.CommandID != cmd.ID || x.AgentID != cmd.AgentID || cmd.Type != "guest_file_put" || cmd.FilePut == nil || cmd.FilePut.ID != x.ID || cmd.FilePut.Topology != x.Topology || cmd.FilePut.Node != x.Node {
		return nil, ErrInvalidRecord
	}
	return encodeGuestDispatch(x.CommandID, cmd, func() ([]byte, error) { return fileOperationCodec.encode(x) })
}
func encodeGuestDispatch(id string, cmd controlplane.AgentCommand, encode func() ([]byte, error)) ([]byte, error) {
	if id == "" || validateCommand(cmd) != nil || cmd.Status != controlplane.AgentCommandStatusQueued || cmd.Attempt != 0 || cmd.LeaseOwner != "" || !cmd.LeaseUntil.IsZero() {
		return nil, ErrInvalidRecord
	}
	return encode()
}

func (r *GuestRepository) CreateFilesystemGuestDispatch(ctx context.Context, execution *controlplane.GuestExecution, file *controlplane.GuestFileOperation, cmd controlplane.AgentCommand) error {
	if _, ok := r.records.(*fileGuestRecords); !ok {
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
	return r.records.update(ctx, kind, id, func(current []byte) ([]byte, error) {
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
	for _, kind := range []guestKind{guestExecutionKind, guestFileKind} {
		rows, err := r.records.list(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
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
