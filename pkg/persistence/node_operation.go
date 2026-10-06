package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"reflect"
)

func ValidateNodeOperationTransition(current, next controlplane.NodeOperation) error {
	if current.RequestedBy != next.RequestedBy || !reflect.DeepEqual(current.Roles, next.Roles) || len(next.Audit) < len(current.Audit) {
		return ErrConflict
	}
	for i := range current.Audit {
		if !reflect.DeepEqual(current.Audit[i], next.Audit[i]) {
			return ErrConflict
		}
	}
	if current.Workspace != next.Workspace || current.ProjectID != next.ProjectID || current.Substrate != next.Substrate || current.Type != next.Type || current.Name != next.Name || current.ExternalID != next.ExternalID || !current.CreatedAt.Equal(next.CreatedAt) {
		return ErrConflict
	}
	if current.CommandID != "" && (current.CommandID != next.CommandID || current.LeaseEpoch != next.LeaseEpoch || current.LeaseOwner != next.LeaseOwner || current.Attempt != next.Attempt) {
		return ErrConflict
	}
	if current.Status != controlplane.NodeOperationStatusQueued && next.Status == controlplane.NodeOperationStatusQueued {
		return ErrConflict
	}
	if current.ID != next.ID || current.AgentID != next.AgentID || current.Topology != next.Topology || current.Node != next.Node || current.Operation != next.Operation || current.ResourceUID != next.ResourceUID || current.Generation != next.Generation || current.ActionID != next.ActionID {
		return ErrConflict
	}
	if current.Status == controlplane.NodeOperationStatusDone || current.Status == controlplane.NodeOperationStatusFailed {
		if !SameNodeOperation(current, next) {
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
