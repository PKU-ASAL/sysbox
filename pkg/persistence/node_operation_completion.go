package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func PreserveNodeOperationDispatch(old, next []byte) ([]byte, error) {
	return preserveGuestDispatch(old, next)
}

// CompleteWithTransition supplies the shared physical write boundary to
// host completion and controller failure rules without duplicating storage.
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
