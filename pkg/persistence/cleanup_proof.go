package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

func (r *Repository) ProveCleanup(ctx context.Context, run controlplane.Run) (*controlplane.CleanupReceipt, error) {
	if run.Op != "destroy" || run.Status != controlplane.RunRunning || run.ID == "" || run.AgentID == "" || run.QueuedAt.IsZero() || run.StartedAt.IsZero() || run.StartedAt.Before(run.QueuedAt) || run.Attempt < 1 || run.LeaseOwner == "" || run.Target != "" {
		return nil, ErrInvalidRecord
	}
	var proof *controlplane.CleanupReceipt
	err := r.update(ctx, run.Topology, func(a *aggregate) error {
		if a.Serial <= 0 {
			return fmt.Errorf("%w: missing persisted state cannot prove cleanup", ErrConflict)
		}
		if len(a.State.Resources) != 0 {
			return fmt.Errorf("%w: managed resources remain after destroy", ErrConflict)
		}
		for _, action := range a.Actions {
			if !runtime.ActionSettled(action) {
				return fmt.Errorf("%w: unfinished action %s", ErrConflict, action.ID)
			}
			if action.RunQueuedAt.After(run.QueuedAt) || action.RunID == run.ID && (!action.RunQueuedAt.Equal(run.QueuedAt) || action.RunRevision != run.Revision) {
				return fmt.Errorf("%w: action belongs to another execution", ErrConflict)
			}
		}
		proof = &controlplane.CleanupReceipt{Version: 1, Topology: run.Topology, TopologyUID: a.State.TopologyUID, AgentID: run.AgentID, RunID: run.ID, QueuedAt: run.QueuedAt, Revision: run.Revision, Attempt: run.Attempt, LeaseOwner: run.LeaseOwner, StateSerial: a.Serial, ObservedAt: time.Now().UTC(), Empty: true}
		return nil
	})
	return proof, err
}
