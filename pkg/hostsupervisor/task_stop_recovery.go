package hostsupervisor

import (
	"context"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"os"
	"time"
)

func sameTaskGrant(a, b controlplane.ExecutionGrant) bool {
	a.State = b.State
	a.ExpiresAt = b.ExpiresAt
	a.StopReceipt = b.StopReceipt
	return a == b
}

// StopAuthorized never starts a task. It persists stop-only authority even
// before provider resolution, so a failed RPC or restart cannot lose the stop.
func (d *Dispatcher) StopAuthorized(ctx context.Context, request controlplane.GuestTaskStop) (TaskRecord, error) {
	var zero TaskRecord
	proof := request.Cancellation
	g := proof.Grant
	if controlplane.VerifyExecutionCancellation(d.IssuerKey, proof) != nil || g.AgentID != d.AgentID || g.ParentEpoch != 0 || request.Topology == "" || request.Node == "" {
		return zero, guesttask.ErrUnauthorized
	}
	ref := TargetRef{Topology: request.Topology, Node: request.Node, Scope: g.Scope}
	h := substrate.ExecutionHandle{ID: g.ActionID, Digest: g.Digest, Epoch: g.Epoch, BootID: g.TargetBootID, NodeID: ref.Node, TopologyUID: g.Scope.TopologyUID, ResourceUID: g.Scope.ResourceUID, Generation: g.Scope.Generation}
	unlock, err := d.lock(ctx, h)
	if err != nil {
		return zero, err
	}
	defer unlock()
	r, err := d.load(h)
	if err == nil {
		if r.Ref != ref || r.RunID != "" || !sameTaskGrant(r.Grant.Grant, g) {
			return r, guesttask.ErrConflict
		}
		if r.StopReceipt != nil {
			r.Reported = false
			return d.report(ctx, r)
		}
	} else if os.IsNotExist(err) {
		r = TaskRecord{Version: 3, Ref: ref, Handle: h, Grant: controlplane.SignedExecutionGrant{Grant: g, BootID: g.TargetBootID}, Result: substrate.ExecResult{Status: substrate.ExecStatusUnknown, RemoteTermination: substrate.RemoteTerminationUnknown}}
	} else {
		return r, err
	}
	r.Cancellation = &proof
	r.CancelRequested = true
	if err = d.save(r); err != nil {
		return r, err
	}
	return d.cancelLocked(ctx, r)
}

func (d *Dispatcher) recoverPendingStops(ctx context.Context) error {
	if d.PendingStops == nil {
		return nil
	}
	var failures []error
	cursor := controlplane.GuestTaskStopCursor{}
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		page, err := d.PendingStops(call, cursor)
		cancel()
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if page.ThroughEpoch < cursor.AfterEpoch || (cursor.ThroughEpoch != 0 && page.ThroughEpoch != cursor.ThroughEpoch) || len(page.Stops)+len(page.Issues) > controlplane.GuestTaskStopPageSize || (page.NextEpoch != 0 && (page.NextEpoch <= cursor.AfterEpoch || page.NextEpoch > page.ThroughEpoch)) {
			return errors.Join(append(failures, guesttask.ErrConflict)...)
		}
		seen := map[uint64]bool{}
		validEpoch := func(epoch uint64) bool {
			if epoch <= cursor.AfterEpoch || epoch > page.ThroughEpoch || (page.NextEpoch != 0 && epoch > page.NextEpoch) || seen[epoch] {
				return false
			}
			seen[epoch] = true
			return true
		}
		for _, issue := range page.Issues {
			if !validEpoch(issue.Epoch) {
				return errors.Join(append(failures, guesttask.ErrConflict)...)
			}
			failures = append(failures, fmt.Errorf("guest stop %s epoch %d: source unavailable", issue.ActionID, issue.Epoch))
		}
		for _, stop := range page.Stops {
			if !validEpoch(stop.Cancellation.Grant.Epoch) {
				return errors.Join(append(failures, guesttask.ErrConflict)...)
			}
			call, cancel := context.WithTimeout(ctx, recoveryTaskBudget)
			_, err := d.StopAuthorized(call, stop)
			cancel()
			if err != nil {
				failures = append(failures, err)
			}
		}
		if page.NextEpoch == 0 {
			return errors.Join(failures...)
		}
		cursor = controlplane.GuestTaskStopCursor{AfterEpoch: page.NextEpoch, ThroughEpoch: page.ThroughEpoch}
	}
}
