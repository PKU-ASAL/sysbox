package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func EncodeNodeOperationDispatch(op controlplane.NodeOperation, cmd controlplane.AgentCommand) ([]byte, error) {
	if op.Status != controlplane.NodeOperationStatusQueued || op.ActionID == "" || op.ResourceUID == "" || op.Generation < 1 || op.CommandID != cmd.ID || op.AgentID != cmd.AgentID || cmd.Type != "node_operation" || cmd.ActionID != op.ActionID || cmd.ResourceUID != op.ResourceUID || cmd.Generation != op.Generation || cmd.LeaseEpoch != op.LeaseEpoch || cmd.Attempt != op.Attempt || cmd.LeaseOwner != op.LeaseOwner {
		return nil, ErrInvalidRecord
	}
	payload := cmd.Operation
	payload.CommandID = cmd.ID
	if !SameNodeOperation(op, payload) {
		return nil, ErrInvalidRecord
	}
	raw, err := encodeGuestDispatch(op.CommandID, cmd, func() ([]byte, error) { return EncodeNodeOperation(op) })
	if err != nil {
		return nil, err
	}
	command, err := EncodeCommandRecord(CommandRecord{Command: cmd})
	if err != nil {
		return nil, err
	}
	return withGuestDispatch(raw, command)
}

func (r *NodeOperationRepository) CreateFilesystemDispatch(ctx context.Context, op controlplane.NodeOperation, cmd controlplane.AgentCommand) error {
	if _, ok := r.records.(*fileGuestRecords); !ok {
		return ErrInvalidRecord
	}
	raw, err := EncodeNodeOperationDispatch(op, cmd)
	if err != nil {
		return err
	}
	return r.records.update(ctx, nodeOperationKind, op.ID, func(current []byte) ([]byte, error) {
		if current != nil {
			return nil, ErrConflict
		}
		return raw, nil
	})
}
