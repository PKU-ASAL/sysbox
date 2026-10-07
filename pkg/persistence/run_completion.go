package persistence

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"time"
)

// CompleteRun merges only result fields under the same lock that checks the
// active execution grant. Controller expiry/cancellation use separate operations.
func (r *RunRepository) CompleteRun(ctx context.Context, agent, id string, report controlplane.RunCompletion) (*controlplane.Run, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !topologyName.MatchString(id) || agent == "" {
		return nil, false, ErrInvalidRecord
	}
	incoming := report.Run
	if incoming.Cleanup != nil && !sameCleanupReceipt(incoming.Cleanup, report.Cleanup) {
		return nil, false, ErrConflict
	}
	if incoming.ID != "" && incoming.ID != id || incoming.AgentID != "" && incoming.AgentID != agent || !incoming.Status.IsTerminal() {
		return nil, false, ErrConflict
	}
	changed := false
	stored, err := r.updateExisting(ctx, id, func(current controlplane.Run) (*controlplane.Run, error) {
		if err := validateLeaseRun(id, current); err != nil {
			return nil, err
		}
		if current.AgentID != agent || current.Attempt < 1 || incoming.Attempt != current.Attempt || incoming.LeaseOwner == "" || incoming.LeaseOwner != current.LeaseOwner {
			return nil, ErrConflict
		}
		if incoming.Topology != "" && incoming.Topology != current.Topology || incoming.ProjectID != "" && incoming.ProjectID != current.ProjectID || incoming.Workspace != "" && incoming.Workspace != current.Workspace || incoming.Revision != "" && incoming.Revision != current.Revision || incoming.SnapshotPath != "" && incoming.SnapshotPath != current.SnapshotPath || incoming.Target != "" && incoming.Target != current.Target || incoming.Op != "" && incoming.Op != current.Op || incoming.Operation != "" && incoming.Operation != current.Operation || !incoming.QueuedAt.IsZero() && !incoming.QueuedAt.Equal(current.QueuedAt) {
			return nil, ErrConflict
		}
		p := report.Projection
		if p.AgentID != "" && p.AgentID != agent || p.Topology != "" && p.Topology != current.Topology || p.Workspace != "" && p.Workspace != current.Workspace {
			return nil, ErrConflict
		}
		if current.Status.IsTerminal() {
			if !sameCleanupReceipt(current.Cleanup, report.Cleanup) {
				return nil, ErrConflict
			}
			if incoming.Assertion != nil {
				a, ea := json.Marshal(current.Assertion)
				b, eb := json.Marshal(incoming.Assertion)
				if ea != nil || eb != nil || string(a) != string(b) {
					return nil, ErrConflict
				}
			}
			// A repeated response may acknowledge the already committed result only.
			if current.Status != incoming.Status || current.Err != incoming.Err || current.Recoverable != incoming.Recoverable || !incoming.EndedAt.IsZero() && !incoming.EndedAt.Equal(current.EndedAt) {
				return nil, ErrConflict
			}
			return nil, nil
		}
		now := r.now()
		if current.Status != controlplane.RunRunning || !current.LeaseUntil.After(now) || !current.DeadlineAt.IsZero() && !current.DeadlineAt.After(now) {
			return nil, ErrConflict
		}
		changed = true
		current.Status = incoming.Status
		current.Err = incoming.Err
		current.Recoverable = incoming.Recoverable
		current.EndedAt = incoming.EndedAt
		if current.EndedAt.IsZero() {
			current.EndedAt = now
		}
		if incoming.Assertion != nil {
			current.Assertion = incoming.Assertion
		}
		if report.Cleanup != nil {
			if ValidateCleanupReceipt(current, report.Cleanup) != nil || report.Cleanup.ObservedAt.After(now.Add(5*time.Second)) {
				return nil, ErrConflict
			}
			proof := *report.Cleanup
			current.Cleanup = &proof
		}
		current = DurableRun(current)
		return &current, nil
	})
	if err != nil {
		return nil, false, err
	}
	return stored, changed, nil
}
