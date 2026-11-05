package persistence

import (
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// CompleteRunFromHost merges a result after the caller has authenticated the
// host, issued grant, original command and immutable Run incarnation.
func CompleteRunFromHost(current, observed controlplane.Run, now time.Time) (*controlplane.Run, error) {
	if validateLeaseRun(current.ID, current) != nil || !observed.Status.IsTerminal() || observed.EndedAt.IsZero() || observed.EndedAt.After(now.Add(5*time.Second)) {
		return nil, ErrConflict
	}
	next := current
	next.Status, next.Err, next.EndedAt = observed.Status, observed.Err, observed.EndedAt
	next.ErrorCode = observed.ErrorCode
	next.Recoverable, next.Assertion, next.Cleanup = observed.Recoverable, observed.Assertion, observed.Cleanup
	// The authenticated host already proved execution stopped. Keeping a future
	// worker deadline blocks observation-only recovery for a completed lease.
	// Retain attempt/owner and the signed original evidence for historical binding.
	next.LeaseUntil = time.Time{}
	controlplane.NormalizeRun(&next)
	if next.Status == controlplane.RunDone {
		if next.ErrorCode != "" || next.Err != "" {
			return nil, ErrConflict
		}
		if next.Op == "destroy" && next.Target == "" && next.Cleanup == nil {
			return nil, ErrConflict
		}
	}
	if next.Cleanup != nil && (ValidateCleanupReceipt(next, next.Cleanup) != nil || next.Cleanup.ObservedAt.After(now.Add(5*time.Second))) {
		return nil, ErrConflict
	}
	if current.Status.IsTerminal() && !(current.Status == controlplane.RunFailed && current.Recoverable && current.Err == "run lease expired") {
		before, _ := json.Marshal(DurableRun(current))
		after, _ := json.Marshal(DurableRun(next))
		if string(before) != string(after) {
			return nil, ErrConflict
		}
	} else if current.Status != controlplane.RunRunning && !current.Status.IsTerminal() {
		return nil, ErrConflict
	}
	next = DurableRun(next)
	return &next, nil
}
