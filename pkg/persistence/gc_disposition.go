package persistence

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const GCDispositionFormatVersion = 9
const gcDispositionAuthorityVersion = 6

type GCFenceDisposition struct {
	Proof         controlplane.GCDispositionProof `json:"proof"`
	HostPublicKey []byte                          `json:"host_public_key"`
	PreparedAt    time.Time                       `json:"prepared_at"`
	ReleasedAt    time.Time                       `json:"released_at,omitempty"`
}

func (r GCReservation) Pending() bool                            { return r.ReceiptHash == "" && r.DispositionHash == "" }
func GCDispositionHash(p controlplane.GCDispositionProof) string { return evidenceHash(p) }

// Prepare persists an immutable decision while the host/authority barrier stays
// held. API callers retire the exact dispatch before releasing its reservation.
func (r *ExecutionGrantRepository) PrepareGCDisposition(ctx context.Context, p controlplane.GCDispositionProof, key ed25519.PublicKey) error {
	if err := controlplane.VerifyGCDisposition(key, p); err != nil {
		return err
	}
	return r.update(ctx, func(a *executionAuthority) error {
		f, ok := a.Fences[p.Request.ID]
		if !ok || f.Receipt == nil || evidenceHash(f.Request) != evidenceHash(p.Request) || evidenceHash(*f.Receipt) != evidenceHash(p.Fence) {
			return ErrConflict
		}
		if f.GCDisposition != nil {
			if evidenceHash(f.GCDisposition.Proof) != evidenceHash(p) {
				return ErrConflict
			}
			return nil
		}
		if f.Release != nil || f.Reconciliation != nil || p.ObservedAt.After(a.ObservedAt) || p.ObservedAt.Before(f.AcceptedAt) {
			return ErrConflict
		}
		g, ok := a.Grants[grantScopeKey(p.Request.Grant.Scope)]
		if !ok || !sameGrantEpoch(g, p.Request.Grant) || g.State != controlplane.GrantRevoked {
			return ErrConflict
		}
		for _, other := range a.Grants {
			if other.AgentID == g.AgentID && other.State != controlplane.GrantStopped && !sameGrantEpoch(other, g) {
				return ErrConflict
			}
		}
		a.Version = gcDispositionAuthorityVersion
		f.GCDisposition = &GCFenceDisposition{Proof: p, HostPublicKey: append([]byte(nil), key...), PreparedAt: a.ObservedAt}
		a.Fences[p.Request.ID] = f
		return nil
	})
}

// CompleteGCDisposition preserves historical outcome unknown. The hash points
// to the independently signed fencing and observation proof in authority.
func (r *Repository) CompleteGCDisposition(ctx context.Context, topology string, p controlplane.GCDispositionProof, key ed25519.PublicKey) error {
	if err := controlplane.VerifyGCDisposition(key, p); err != nil {
		return err
	}
	if p.Request.Topology != topology {
		return ErrConflict
	}
	return r.updateGC(ctx, topology, true, func(a *aggregate) error {
		stored, ok := a.GC[p.Request.Grant.CommandID]
		if !ok {
			return ErrNotFound
		}
		if stored.AgentID != p.Request.Grant.AgentID || stored.Candidate != p.Candidate || stored.ReceiptHash != "" {
			return ErrConflict
		}
		hash := GCDispositionHash(p)
		if stored.DispositionHash != "" {
			if stored.DispositionHash != hash {
				return ErrConflict
			}
			return nil
		}
		if evidenceHash(stored) != p.Request.JournalSHA256 {
			return ErrConflict
		}
		a.Format = GCDispositionFormatVersion
		stored.DispositionHash = hash
		a.GC[stored.ID] = stored
		return nil
	})
}

// Call with the canonical reservation hash only after the original command is
// durably retired. Command-bound authority storage validates that retirement
// again under its lock. The host barrier is released last, making retries safe.
func (r *ExecutionGrantRepository) FinishGCDisposition(ctx context.Context, p controlplane.GCDispositionProof, canonicalHash string) error {
	return r.update(ctx, func(a *executionAuthority) error {
		f, ok := a.Fences[p.Request.ID]
		hash := GCDispositionHash(p)
		if !ok || f.GCDisposition == nil || canonicalHash != hash || evidenceHash(f.GCDisposition.Proof) != hash {
			return ErrConflict
		}
		if !f.GCDisposition.ReleasedAt.IsZero() {
			return nil
		}
		g, ok := a.Grants[grantScopeKey(p.Request.Grant.Scope)]
		if !ok || !sameGrantEpoch(g, p.Request.Grant) || g.State != controlplane.GrantRevoked {
			return ErrConflict
		}
		for _, other := range a.Grants {
			if other.AgentID == g.AgentID && other.State != controlplane.GrantStopped && !sameGrantEpoch(other, g) {
				return ErrConflict
			}
		}
		g.State = controlplane.GrantStopped
		g.StopReceipt = "gc-fenced:" + hash
		a.Grants[grantScopeKey(g.Scope)] = g
		if a.Stops == nil {
			a.Stops = map[uint64]stoppedExecutionGrant{}
		}
		a.Stops[g.Epoch] = stoppedExecutionGrant{Grant: g, Receipt: g.StopReceipt}
		f.GCDisposition.ReleasedAt = a.ObservedAt
		a.Fences[p.Request.ID] = f
		barrier := a.Maintenance[f.BarrierID]
		barrier.Released = true
		a.Maintenance[f.BarrierID] = barrier
		return nil
	})
}
func validateGCDisposition(f HostFenceRecord, a *executionAuthority) error {
	d := f.GCDisposition
	if d == nil {
		return nil
	}
	if a.Version != gcDispositionAuthorityVersion {
		return ErrCorruptRecord
	}
	if f.Release != nil || f.Reconciliation != nil || f.Receipt == nil || evidenceHash(d.Proof.Request) != evidenceHash(f.Request) || evidenceHash(d.Proof.Fence) != evidenceHash(*f.Receipt) || controlplane.VerifyGCDisposition(d.HostPublicKey, d.Proof) != nil || d.PreparedAt.Before(d.Proof.ObservedAt) || d.PreparedAt.After(a.ObservedAt) {
		return ErrCorruptRecord
	}
	if !d.ReleasedAt.IsZero() {
		stop, ok := a.Stops[f.Request.Grant.Epoch]
		if !ok || !sameGrantEpoch(stop.Grant, f.Request.Grant) || stop.Receipt != "gc-fenced:"+GCDispositionHash(d.Proof) || d.ReleasedAt.Before(d.PreparedAt) || d.ReleasedAt.After(a.ObservedAt) {
			return fmt.Errorf("%w: GC disposition release", ErrCorruptRecord)
		}
	}
	return nil
}
func (f HostFenceRecord) released() bool {
	return f.Release != nil || (f.GCDisposition != nil && !f.GCDisposition.ReleasedAt.IsZero())
}
