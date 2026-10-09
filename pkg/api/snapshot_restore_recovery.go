package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/persistence"
)

// finishCommittedRestores never initiates a restore. It retries release only
// from a committed receipt and the exact existing barrier; a missing/corrupt
// authority record is an error, not grounds to invent replacement authority.
func (s *Server) finishCommittedRestores(ctx context.Context, topology string) error {
	repo, err := s.actionRepository(topology)
	if err != nil {
		return err
	}
	receipts, err := repo.ListSnapshotRestoreReceipts(ctx, topology)
	if err != nil {
		return err
	}
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	var failures []error
	for _, receipt := range receipts {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		barrier, err := authority.InspectMaintenance(call, receipt.Request.ID)
		if err == nil {
			digest, digestErr := receipt.Request.Digest()
			if digestErr != nil || barrier.TopologyUID != receipt.Request.TopologyUID || barrier.Epoch != receipt.Request.MaintenanceEpoch || barrier.RequestDigest != digest {
				err = persistence.ErrConflict
			} else if !barrier.Released {
				// Already committed receipt: release does not need another state
				// write and cannot invalidate a subsequently issued barrier.
				err = authority.EndMaintenance(call, *barrier)
			}
		}
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("restore %s release: %w", receipt.Request.ID, err))
		}
	}
	return errors.Join(failures...)
}
