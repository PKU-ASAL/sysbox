package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
)

func ValidateNodeOperationTransition(current, next controlplane.NodeOperation) error {
	if current.ID != next.ID || current.AgentID != next.AgentID || current.Topology != next.Topology || current.Node != next.Node || current.Operation != next.Operation || current.ResourceUID != next.ResourceUID || current.Generation != next.Generation || current.ActionID != next.ActionID {
		return ErrConflict
	}
	if current.Status == controlplane.NodeOperationStatusDone || current.Status == controlplane.NodeOperationStatusFailed {
		if next.Status != current.Status || next.Err != current.Err {
			return ErrConflict
		}
	}
	switch next.Status {
	case controlplane.NodeOperationStatusQueued, controlplane.NodeOperationStatusRunning, controlplane.NodeOperationStatusDone, controlplane.NodeOperationStatusFailed:
	default:
		return ErrInvalidRecord
	}
	if current.Status == controlplane.NodeOperationStatusQueued && (next.Status != controlplane.NodeOperationStatusQueued && next.Status != controlplane.NodeOperationStatusRunning && next.Status != controlplane.NodeOperationStatusDone && next.Status != controlplane.NodeOperationStatusFailed) {
		return ErrConflict
	}
	return nil
}
