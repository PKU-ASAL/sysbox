package persistence

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"time"
)

const GCAdmissionFormatVersion = 12

func (r *Repository) ClaimGCExecution(ctx context.Context, topology string, c controlplane.GCCandidate, g controlplane.ExecutionGrant) error {
	digest, err := c.Digest()
	if err != nil {
		return err
	}
	if g.Operation != "gc_delete" || g.Digest != digest || g.Scope != (controlplane.ExecutionScope{TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: c.Generation}) || g.Epoch == 0 || g.ActionID != g.CommandID {
		return ErrConflict
	}
	return r.updateGC(ctx, topology, true, func(a *aggregate) error {
		return claimGCExecution(a, c, g)
	})
}

func claimGCExecution(a *aggregate, c controlplane.GCCandidate, g controlplane.ExecutionGrant) error {
	reservation, ok := a.GC[g.CommandID]
	if !ok || !reservation.Pending() || reservation.Candidate != c || reservation.AgentID != g.AgentID || reservation.AdmissionDigest != "" || nextGC(a) != g.CommandID {
		return ErrConflict
	}
	if a.Format != GCAdmissionFormatVersion && a.Format != AdoptionRecoveryFormatVersion && a.Format != LegacyAdoptionRecoveryFormatVersion {
		a.GCReferenceFormat = a.Format
		a.Format = GCAdmissionFormatVersion
	}
	reservation.AdmissionDigest = evidenceHash(g)
	a.GC[g.CommandID] = reservation
	return nil
}

func ApplyGCExecutionClaim(raw, authorityRaw []byte, c controlplane.GCCandidate, g controlplane.ExecutionGrant) ([]byte, error) {
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, err
	}
	auth, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if auth.ObservedAt.After(now) {
		now = auth.ObservedAt
	}
	current, ok := auth.Grants[grantScopeKey(g.Scope)]
	if !ok || !sameGrantEpoch(current, g) || current.State != controlplane.GrantActive || auth.Sessions[g.AgentID].ID != g.SessionID || !current.ExpiresAt.After(now) || auth.maintenanceBlocks(g.Scope.TopologyUID) || auth.hostFenceBlocks(g.AgentID) {
		return nil, ErrConflict
	}
	digest, err := c.Digest()
	if err != nil || g.Digest != digest || g.Operation != "gc_delete" || g.ActionID != g.CommandID || g.Scope != (controlplane.ExecutionScope{TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: c.Generation}) {
		return nil, ErrConflict
	}
	if err = claimGCExecution(a, c, g); err != nil {
		return nil, err
	}
	if err = validateAggregate(a, ""); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}
func (r *Repository) ClaimGCExecutionWithAuthority(ctx context.Context, topology string, c controlplane.GCCandidate, g controlplane.ExecutionGrant, authority []byte) error {
	return r.updateGC(ctx, topology, true, func(a *aggregate) error {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		next, err := ApplyGCExecutionClaim(raw, authority, c, g)
		if err != nil {
			return err
		}
		updated, err := decodeAggregate(next)
		if err != nil {
			return err
		}
		*a = *updated
		return nil
	})
}

// The immutable reservation identity excludes a later one-shot admission bit.
func GCReservationDigest(r GCReservation) string { r.AdmissionDigest = ""; return evidenceHash(r) }
