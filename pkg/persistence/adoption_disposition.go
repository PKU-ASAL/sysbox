package persistence

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const adoptionDispositionAuthorityVersion = 7

type AdoptionFenceDisposition struct {
	OperationResult string                                `json:"operation_result"`
	Proof           controlplane.AdoptionDispositionProof `json:"proof"`
	HostPublicKey   []byte                                `json:"host_public_key"`
	PreparedAt      time.Time                             `json:"prepared_at"`
	ReleasedAt      time.Time                             `json:"released_at,omitempty"`
}

func AdoptionDispositionHash(p controlplane.AdoptionDispositionProof) string { return evidenceHash(p) }

// Prepare persists an immutable decision while the host/authority barrier stays
// held. API callers retire the exact dispatch before releasing its reservation.
func (r *ExecutionGrantRepository) PrepareAdoptionDisposition(ctx context.Context, p controlplane.AdoptionDispositionProof, key ed25519.PublicKey) error {
	if err := controlplane.VerifyAdoptionDisposition(key, p); err != nil {
		return err
	}
	return r.update(ctx, func(a *executionAuthority) error {
		f, ok := a.Fences[p.Request.ID]
		if !ok || f.Receipt == nil || evidenceHash(f.Request) != evidenceHash(p.Request) || evidenceHash(*f.Receipt) != evidenceHash(p.Fence) {
			return ErrConflict
		}
		if f.AdoptionDisposition != nil {
			if evidenceHash(f.AdoptionDisposition.Proof) != evidenceHash(p) {
				return ErrConflict
			}
			return nil
		}
		if f.GCDisposition != nil || f.Release != nil || f.Reconciliation != nil || p.ObservedAt.After(a.ObservedAt) || p.ObservedAt.Before(f.AcceptedAt) {
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
		a.Version = adoptionDispositionAuthorityVersion
		f.AdoptionDisposition = &AdoptionFenceDisposition{OperationResult: "unknown", Proof: p, HostPublicKey: append([]byte(nil), key...), PreparedAt: a.ObservedAt}
		a.Fences[p.Request.ID] = f
		return nil
	})
}

// Call with the canonical reservation hash only after the original command is
// durably retired. Command-bound authority storage validates that retirement
// again under its lock. The host barrier is released last, making retries safe.
func (r *ExecutionGrantRepository) FinishAdoptionDisposition(ctx context.Context, p controlplane.AdoptionDispositionProof, canonicalHash string) error {
	return r.update(ctx, func(a *executionAuthority) error {
		f, ok := a.Fences[p.Request.ID]
		hash := AdoptionDispositionHash(p)
		if !ok || f.AdoptionDisposition == nil || canonicalHash != hash || evidenceHash(f.AdoptionDisposition.Proof) != hash {
			return ErrConflict
		}
		if !f.AdoptionDisposition.ReleasedAt.IsZero() {
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
		g.StopReceipt = "adoption-fenced:" + hash
		a.Grants[grantScopeKey(g.Scope)] = g
		if a.Stops == nil {
			a.Stops = map[uint64]stoppedExecutionGrant{}
		}
		a.Stops[g.Epoch] = stoppedExecutionGrant{Grant: g, Receipt: g.StopReceipt}
		f.AdoptionDisposition.ReleasedAt = a.ObservedAt
		a.Fences[p.Request.ID] = f
		barrier := a.Maintenance[f.BarrierID]
		barrier.Released = true
		a.Maintenance[f.BarrierID] = barrier
		return nil
	})
}
func validateAdoptionDisposition(f HostFenceRecord, a *executionAuthority) error {
	d := f.AdoptionDisposition
	if d == nil {
		return nil
	}
	if a.Version != adoptionDispositionAuthorityVersion || d.OperationResult != "unknown" {
		return ErrCorruptRecord
	}
	if f.GCDisposition != nil || f.Release != nil || f.Reconciliation != nil || f.Receipt == nil || evidenceHash(d.Proof.Request) != evidenceHash(f.Request) || evidenceHash(d.Proof.Fence) != evidenceHash(*f.Receipt) || controlplane.VerifyAdoptionDisposition(d.HostPublicKey, d.Proof) != nil || d.PreparedAt.Before(d.Proof.ObservedAt) || d.PreparedAt.After(a.ObservedAt) {
		return ErrCorruptRecord
	}
	if !d.ReleasedAt.IsZero() {
		stop, ok := a.Stops[f.Request.Grant.Epoch]
		if !ok || !sameGrantEpoch(stop.Grant, f.Request.Grant) || stop.Receipt != "adoption-fenced:"+AdoptionDispositionHash(d.Proof) || d.ReleasedAt.Before(d.PreparedAt) || d.ReleasedAt.After(a.ObservedAt) {
			return fmt.Errorf("%w: GC disposition release", ErrCorruptRecord)
		}
	}
	return nil
}
