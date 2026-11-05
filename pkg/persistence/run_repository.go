package persistence

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// RunStorage must lock/read/transform/write a complete current run in one
// critical section. A nil replacement leaves the run unchanged. No callback
// result may be published if the transaction or filesystem write fails.
// LiveRunLeaseRequired marks worker-authorized transitions. Storage must
// recheck the original lease at its write boundary; controller expiry is separate.
type liveRunLeaseKey struct{}

func LiveRunLeaseRequired(ctx context.Context) bool {
	required, _ := ctx.Value(liveRunLeaseKey{}).(bool)
	return required
}

func requireLiveRunLease(ctx context.Context) context.Context {
	return context.WithValue(ctx, liveRunLeaseKey{}, true)
}

type RunStorage interface {
	UpdateRun(context.Context, string, func(*controlplane.Run) (*controlplane.Run, error)) (*controlplane.Run, error)
}

type RunRepository struct {
	storage RunStorage
	now     func() time.Time
}

func NewRunRepository(storage RunStorage) *RunRepository {
	return &RunRepository{storage: storage, now: func() time.Time { return time.Now().UTC() }}
}

// DurableRun removes transient input values at the storage boundary, even for
// callers which bypass Jobs.persist. It never mutates the caller's input map.
func DurableRun(run controlplane.Run) controlplane.Run {
	controlplane.NormalizeRun(&run)
	run.Inputs = nil

	if run.HostCompletion != nil {
		proof := *run.HostCompletion
		proof.Evidence = append([]byte(nil), proof.Evidence...)
		proof.Receipt.Signature = append([]byte(nil), proof.Receipt.Signature...)
		run.HostCompletion = &proof
	}
	if run.Cleanup != nil {
		proof := *run.Cleanup
		run.Cleanup = &proof
	}
	return run
}

type runClaimIdentityKey struct{}

// WithRunClaimIdentity pins a preflighted run so a concurrent restart cannot
// claim a different execution between input verification and the atomic claim.
func WithRunClaimIdentity(ctx context.Context, run controlplane.Run) context.Context {
	run = DurableRun(run)
	run.Assertion = nil
	run.Err = ""
	run.ErrorCode = ""
	run.LeaseOwner = ""
	return context.WithValue(ctx, runClaimIdentityKey{}, run)
}

func (r *RunRepository) updateExisting(ctx context.Context, id string, fn func(controlplane.Run) (*controlplane.Run, error)) (*controlplane.Run, error) {
	return r.storage.UpdateRun(ctx, id, func(current *controlplane.Run) (*controlplane.Run, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		return fn(*current)
	})
}

// SaveRun protects the lease authority even when a caller writes a whole run
// snapshot. Only Claim/Renew/Expire and explicit Restart may change that authority.
func (r *RunRepository) SaveRun(ctx context.Context, run controlplane.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	run = DurableRun(run)
	if err := validateLeaseRun(run.ID, run); err != nil {
		return ErrInvalidRecord
	}
	_, err := r.storage.UpdateRun(ctx, run.ID, func(current *controlplane.Run) (*controlplane.Run, error) {
		if current == nil {
			if run.Cleanup != nil || run.HostCompletion != nil {
				return nil, ErrConflict
			}
			return &run, nil
		}
		if err := validateLeaseRun(run.ID, *current); err != nil {
			return nil, err
		}
		old := DurableRun(*current)
		beforeProof, _ := json.Marshal(old.HostCompletion)
		afterProof, _ := json.Marshal(run.HostCompletion)
		if string(beforeProof) != string(afterProof) {
			return nil, ErrConflict
		}

		if !sameCleanupReceipt(old.Cleanup, run.Cleanup) {
			return nil, ErrConflict
		}
		if old.RequestFingerprint != run.RequestFingerprint {
			return nil, ErrConflict
		}
		if old.Topology != run.Topology || old.ProjectID != run.ProjectID || old.Workspace != run.Workspace || old.Operation != run.Operation || !old.QueuedAt.Equal(run.QueuedAt) || old.Attempt != run.Attempt || old.LeaseOwner != run.LeaseOwner {
			return nil, ErrConflict
		}
		if old.Status != controlplane.RunQueued && (old.AgentID != run.AgentID || old.Revision != run.Revision || old.SnapshotPath != run.SnapshotPath || old.Target != run.Target || old.OperationKey != run.OperationKey || old.ParentID != run.ParentID || old.UnsafeState != run.UnsafeState || !old.DeadlineAt.Equal(run.DeadlineAt)) {
			return nil, ErrConflict
		}
		if run.LeaseUntil.After(old.LeaseUntil) {
			return nil, ErrConflict
		}
		run.LeaseUntil = old.LeaseUntil
		if old.Status.IsTerminal() {
			a, err := json.Marshal(old)
			if err != nil {
				return nil, ErrCorruptRecord
			}
			b, err := json.Marshal(run)
			if err != nil {
				return nil, ErrInvalidRecord
			}
			if string(a) != string(b) {
				return nil, ErrConflict
			}
			return nil, nil
		}
		if run.Status.Rank() < old.Status.Rank() {
			return nil, ErrConflict
		}
		if old.Status == controlplane.RunRunning && !old.LeaseUntil.IsZero() && !old.LeaseUntil.After(r.now()) && run.Status != controlplane.RunFailed && run.Status != controlplane.RunCancelled {
			return nil, ErrConflict
		}
		return &run, nil
	})
	return err
}

// RestartRun is an explicit new execution of a coalesced apply. QueuedAt is the
// immutable incarnation token carried by existing v1 run snapshots.
func (r *RunRepository) RestartRun(ctx context.Context, next controlplane.Run) (*controlplane.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next = DurableRun(next)
	if next.Cleanup != nil || next.HostCompletion != nil {
		return nil, ErrConflict
	}
	if validateLeaseRun(next.ID, next) != nil || next.Status != controlplane.RunQueued || next.Operation != "apply" || next.OperationKey == "" || next.QueuedAt.IsZero() || next.Attempt != 0 || !next.LeaseUntil.IsZero() {
		return nil, ErrInvalidRecord
	}
	return r.storage.UpdateRun(ctx, next.ID, func(current *controlplane.Run) (*controlplane.Run, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		if err := validateLeaseRun(next.ID, *current); err != nil {
			return nil, err
		}
		old := DurableRun(*current)
		if !old.Status.IsTerminal() || old.Topology != next.Topology || old.Operation != next.Operation || old.OperationKey != next.OperationKey || !next.QueuedAt.After(old.QueuedAt) {
			return nil, ErrConflict
		}
		return &next, nil
	})
}

func validLeaseRequest(id, agent, owner string, ttl time.Duration) error {
	if !topologyName.MatchString(id) || strings.TrimSpace(agent) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return ErrInvalidRecord
	}
	return nil
}

func validateLeaseRun(id string, run controlplane.Run) error {
	if run.ID != id || !topologyName.MatchString(run.ID) || !topologyName.MatchString(run.Topology) || run.Attempt < 0 || (!run.Status.IsActive() && !run.Status.IsTerminal()) || len(run.Inputs) > 0 || run.Op != "" && run.Operation != "" && run.Op != run.Operation {
		return ErrCorruptRecord
	}
	return nil
}

func deadlinePassed(run controlplane.Run, now time.Time) bool {
	return !run.DeadlineAt.IsZero() && !run.DeadlineAt.After(now)
}

func leaseEnd(run controlplane.Run, now time.Time, ttl time.Duration) time.Time {
	end := now.Add(ttl)
	if !run.DeadlineAt.IsZero() && end.After(run.DeadlineAt) {
		end = run.DeadlineAt
	}
	return end
}

func (r *RunRepository) ClaimRun(ctx context.Context, id, agent, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := validLeaseRequest(id, agent, owner, ttl); err != nil {
		return nil, false, err
	}
	claimed := false
	out, err := r.updateExisting(ctx, id, func(run controlplane.Run) (*controlplane.Run, error) {
		if err := validateLeaseRun(id, run); err != nil {
			return nil, err
		}
		if expected, ok := ctx.Value(runClaimIdentityKey{}).(controlplane.Run); ok && !controlplane.SameRunIncarnation(expected, run) {
			return nil, ErrConflict
		}
		now := r.now()
		// A lost response may recover the exact live claim, never renew it.
		if run.Status == controlplane.RunRunning && run.AgentID == agent && run.LeaseOwner == owner && run.LeaseUntil.After(now) && !deadlinePassed(run, now) {
			claimed = true
			return nil, nil
		}
		if deadlinePassed(run, now) || !run.CanBeClaimedBy(agent, now) {
			return nil, nil
		}
		if run.Attempt == int(^uint(0)>>1) {
			return nil, ErrConflict
		}
		run.MarkRunning(owner, ttl, now)
		run.LeaseUntil = leaseEnd(run, now, ttl)
		claimed = true
		return &run, nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, claimed, nil
}

func (r *RunRepository) RenewRunLease(ctx context.Context, id, agent, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	ctx = requireLiveRunLease(ctx)
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := validLeaseRequest(id, agent, owner, ttl); err != nil {
		return nil, false, err
	}
	renewed := false
	out, err := r.updateExisting(ctx, id, func(run controlplane.Run) (*controlplane.Run, error) {
		if err := validateLeaseRun(id, run); err != nil {
			return nil, err
		}
		now := r.now()
		if deadlinePassed(run, now) || !run.CanRenewLease(agent, owner) || !run.LeaseUntil.After(now) {
			return nil, nil
		}
		end := leaseEnd(run, now, ttl)
		renewed = true
		if !end.After(run.LeaseUntil) {
			return nil, nil
		}
		run.LeaseUntil = end
		return &run, nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, renewed, nil
}

func (r *RunRepository) ExpireRunLease(ctx context.Context, id string, observedAt time.Time) (*controlplane.Run, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !topologyName.MatchString(id) || observedAt.IsZero() {
		return nil, false, ErrInvalidRecord
	}
	expired := false
	out, err := r.updateExisting(ctx, id, func(run controlplane.Run) (*controlplane.Run, error) {
		if err := validateLeaseRun(id, run); err != nil {
			return nil, err
		}
		if run.Status != controlplane.RunRunning || run.LeaseUntil.IsZero() || run.LeaseUntil.After(observedAt) {
			return nil, nil
		}
		run.MarkLeaseExpired(observedAt)
		expired = true
		return &run, nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, expired, nil
}
