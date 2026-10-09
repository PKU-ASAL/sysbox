package api

import (
	"context"
	"errors"

	"github.com/oslab/sysbox/pkg/persistence"
)

type snapshotRestoreResult struct {
	Status  string                              `json:"status"`
	Request persistence.SnapshotRestoreRequest  `json:"request"`
	Receipt *persistence.SnapshotRestoreReceipt `json:"receipt,omitempty"`
}

// The caller keeps the exact preflight request across retries. A pending result
// retains its durable barrier; context cancellation never unfreezes execution.
func (s *Server) restoreSnapshotWorkflow(ctx context.Context, topology string, request persistence.SnapshotRestoreRequest) (snapshotRestoreResult, error) {
	result := snapshotRestoreResult{Request: request}
	repo, err := s.actionRepository(topology)
	if err != nil {
		return result, err
	}
	digest, err := request.Digest()
	if err != nil {
		return result, err
	}
	prior, readErr := repo.ReadSnapshotRestoreReceipt(ctx, topology, request.ID)
	if readErr != nil && !errors.Is(readErr, persistence.ErrNotFound) {
		return result, readErr
	}
	if readErr == nil {
		expected := request
		if expected.MaintenanceEpoch == 0 {
			expected.MaintenanceEpoch = prior.Request.MaintenanceEpoch
		}
		if prior.Request != expected {
			return result, persistence.ErrConflict
		}
	} else {
		plan, err := repo.PlanSnapshotRestore(ctx, topology, request.SnapshotID, request.ID, request.MaintenanceEpoch)
		if err != nil {
			return result, err
		}
		if plan != request {
			return result, persistence.ErrConflict
		}
	}
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if request.MaintenanceEpoch != 0 {
		existing, err := authority.InspectMaintenance(ctx, request.ID)
		if err != nil {
			return result, err
		}
		if existing.Epoch != request.MaintenanceEpoch || existing.TopologyUID != request.TopologyUID || existing.RequestDigest != digest {
			return result, persistence.ErrConflict
		}
	}
	barrier, err := authority.BeginMaintenance(ctx, request.ID, request.TopologyUID, digest)
	if err != nil {
		return result, err
	}
	if request.MaintenanceEpoch != 0 && request.MaintenanceEpoch != barrier.Epoch {
		return result, persistence.ErrConflict
	}
	request.MaintenanceEpoch = barrier.Epoch
	result.Request = request
	if readErr != nil {
		if barrier.Released {
			return result, persistence.ErrConflict
		}
		if err := s.workspaceService().ensureTopologyQuiescent(ctx, topology); err != nil {
			if errors.Is(err, persistence.ErrConflict) {
				result.Status = "waiting_for_stop"
				return result, nil
			}
			return result, err
		}
	}
	receipt, err := s.commitSnapshotRestore(ctx, topology, request)
	if err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			result.Status = "waiting_for_stop_or_reconciliation"
			return result, nil
		}
		return result, err
	}
	result.Receipt = &receipt
	if err := authority.EndMaintenance(ctx, *barrier); err != nil {
		result.Status = "committed_waiting_for_release"
		// The restore is already durable. Preserve its receipt in a pending
		// response so retry finishes release instead of suggesting rollback.
		return result, nil
	}
	result.Status = "restored"
	return result, nil
}
