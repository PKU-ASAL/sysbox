package persistence

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

const OwnershipTargetFormatVersion = 11

type OwnershipTargetReservation struct {
	DispositionHash string                             `json:"disposition_hash,omitempty"`
	Aborted         bool                               `json:"aborted,omitempty"`
	Preparation     *controlplane.OwnershipPreparation `json:"preparation,omitempty"`
	PreparationKey  []byte                             `json:"preparation_key,omitempty"`
	Plan            controlplane.OwnershipTransferPlan `json:"plan"`
	CompletedHash   string                             `json:"completed_hash,omitempty"`
}

func ownershipPending(a *aggregate) bool {
	for _, v := range a.OwnershipTargets {
		if v.CompletedHash == "" && v.DispositionHash == "" && !v.Aborted {
			return true
		}
	}
	return false
}
func validateOwnershipTargets(a *aggregate) error {
	if len(a.OwnershipTargets) > 0 && a.Format != OwnershipTargetFormatVersion && a.Format != GCAdmissionFormatVersion && a.Format != AdoptionRecoveryFormatVersion && a.Format != LegacyAdoptionRecoveryFormatVersion {
		return ErrCorruptRecord
	}
	pending := 0
	for id, v := range a.OwnershipTargets {
		if v.DispositionHash != "" {
			if (a.Format != AdoptionRecoveryFormatVersion && a.Format != LegacyAdoptionRecoveryFormatVersion) || v.CompletedHash != "" || v.Aborted {
				return ErrCorruptRecord
			}
			found := false
			for _, r := range a.AdoptionRecoveries {
				if r.Proof.Adoption.ID == id && AdoptionDispositionHash(r.Proof) == v.DispositionHash {
					found = true
				}
			}
			if !found {
				return ErrCorruptRecord
			}
		}
		if id != v.Plan.ID || v.Plan.Validate() != nil {
			return ErrCorruptRecord
		}
		if v.Preparation != nil && (controlplane.VerifyOwnershipPreparation(v.PreparationKey, *v.Preparation) != nil || evidenceHash(v.Preparation.Plan) != evidenceHash(v.Plan)) {
			return ErrCorruptRecord
		}
		if v.CompletedHash == "" && v.DispositionHash == "" && !v.Aborted {
			pending++
			if a.State.TopologyUID != v.Plan.Target.TopologyUID || a.Serial != v.Plan.TargetSerial {
				return ErrCorruptRecord
			}
		} else if !v.Aborted && len(v.CompletedHash) != 64 && len(v.DispositionHash) != 64 {
			return ErrCorruptRecord
		}
	}
	if pending > 1 {
		return ErrCorruptRecord
	}
	return nil
}
func (r *Repository) ReserveOwnershipTarget(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
	if p.Validate() != nil {
		return ErrInvalidRecord
	}
	return r.updateOwnership(ctx, p.TargetTopology, func(a *aggregate) error {
		if old, ok := a.OwnershipTargets[p.ID]; ok {
			if old.Aborted || old.DispositionHash != "" {
				return ErrConflict
			}
			if evidenceHash(old.Plan) != evidenceHash(p) {
				return ErrConflict
			}
			return nil
		}
		if ownershipPending(a) || gcPending(a) || a.State.TopologyUID != p.Target.TopologyUID || a.Serial != p.TargetSerial {
			return ErrConflict
		}
		addr, err := address.Parse(p.TargetAddress)
		if err != nil {
			return err
		}
		if a.State.FindResource(addr) != nil {
			return ErrConflict
		}
		for _, v := range a.State.Resources {
			if v.ResourceUID == p.Target.ResourceUID || (v.Driver == p.Provider && v.ExternalID == p.ExternalID) {
				return ErrConflict
			}
		}
		for _, action := range a.Actions {
			if !runtime.ActionSettled(action) {
				return ErrConflict
			}
		}
		if a.OwnershipTargets == nil {
			a.OwnershipTargets = map[string]OwnershipTargetReservation{}
		}
		a.OwnershipTargets[p.ID] = OwnershipTargetReservation{Plan: p}
		if a.Format != GCAdmissionFormatVersion && a.Format != AdoptionRecoveryFormatVersion && a.Format != LegacyAdoptionRecoveryFormatVersion {
			a.Format = OwnershipTargetFormatVersion
		}
		return nil
	})
}

// Caller authenticates the host transfer result before invoking this canonical
// state transition. Retirement proof alone is not provider completion evidence.
func (r *Repository) CommitOwnershipTarget(ctx context.Context, proof controlplane.OwnershipRetirementProof, key ed25519.PublicKey, resource state.Resource) error {
	if err := controlplane.VerifyOwnershipRetirement(key, proof); err != nil {
		return err
	}
	p := proof.Plan
	addr, err := address.Parse(p.TargetAddress)
	if err != nil {
		return err
	}
	if !resource.Address.Equal(addr) || resource.Driver != p.Provider || resource.ExternalID != p.ExternalID || resource.OwnerUID != p.Target.OwnerUID || resource.ResourceUID != p.Target.ResourceUID || resource.Generation != p.Target.Generation {
		return ErrConflict
	}
	stable := resource
	stable.CreatedAt = time.Time{}
	stable.UpdatedAt = time.Time{}
	completion := evidenceHash(struct {
		Proof    controlplane.OwnershipRetirementProof
		Resource state.Resource
	}{proof, stable})
	return r.updateOwnership(ctx, p.TargetTopology, func(a *aggregate) error { return commitOwnershipTarget(a, p, addr, resource, completion) })
}
func commitOwnershipTarget(a *aggregate, p controlplane.OwnershipTransferPlan, addr address.Address, resource state.Resource, completion string) error {

	reservation, ok := a.OwnershipTargets[p.ID]
	if !ok || reservation.Aborted || reservation.DispositionHash != "" || evidenceHash(reservation.Plan) != evidenceHash(p) {
		return ErrConflict
	}
	if reservation.CompletedHash != "" {
		if reservation.CompletedHash != completion {
			return ErrConflict
		}
		return nil
	}
	if a.State.TopologyUID != p.Target.TopologyUID || a.Serial != p.TargetSerial || a.State.FindResource(addr) != nil || gcPending(a) {
		return ErrConflict
	}
	a.State.AddResource(resource)
	a.Serial++
	a.UpdatedAt = time.Now().UTC()
	reservation.CompletedHash = completion
	a.OwnershipTargets[p.ID] = reservation
	return nil

}

func (r *Repository) updateOwnership(ctx context.Context, topology string, fn func(*aggregate) error) error {
	if !topologyName.MatchString(topology) || (r.boundTopology != "" && r.boundTopology != topology) {
		return ErrInvalidRecord
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.backend.update(ctx, topology, func(a *aggregate) error {
		if err := validateAggregate(a, topology); err != nil {
			return err
		}
		if err := fn(a); err != nil {
			return err
		}
		return validateAggregate(a, topology)
	})
}
func (r *Repository) ReadOwnershipTarget(ctx context.Context, topology, id string) (OwnershipTargetReservation, error) {
	var result OwnershipTargetReservation
	err := r.update(ctx, topology, func(a *aggregate) error {
		v, ok := a.OwnershipTargets[id]
		if !ok {
			return ErrNotFound
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &result)
	})
	return result, err
}

func (r *Repository) RecordOwnershipPreparation(ctx context.Context, p controlplane.OwnershipPreparation, key ed25519.PublicKey) error {
	if err := controlplane.VerifyOwnershipPreparation(key, p); err != nil {
		return err
	}
	return r.updateOwnership(ctx, p.Plan.TargetTopology, func(a *aggregate) error {
		v, ok := a.OwnershipTargets[p.Plan.ID]
		if !ok || v.Aborted || v.DispositionHash != "" || evidenceHash(v.Plan) != evidenceHash(p.Plan) || v.CompletedHash != "" || v.DispositionHash != "" {
			return ErrConflict
		}
		if v.Preparation != nil {
			if evidenceHash(v.Preparation) != evidenceHash(p) {
				return ErrConflict
			}
			return nil
		}
		v.Preparation = &p
		v.PreparationKey = append([]byte(nil), key...)
		a.OwnershipTargets[p.Plan.ID] = v
		return nil
	})
}

// AbortOwnershipTarget is called only while source authority is locked and the
// caller has proved that no source retirement was committed. Its tombstone
// prevents an old prepared request from reusing this reservation.
func (r *Repository) AbortOwnershipTarget(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
	return r.updateOwnership(ctx, p.TargetTopology, func(a *aggregate) error {
		v, ok := a.OwnershipTargets[p.ID]
		if !ok || evidenceHash(v.Plan) != evidenceHash(p) {
			return ErrConflict
		}
		if v.CompletedHash != "" || v.DispositionHash != "" {
			return ErrConflict
		}
		v.Aborted = true
		a.OwnershipTargets[p.ID] = v
		return nil
	})
}
func (r *Repository) HasOwnershipRetirement(ctx context.Context, topology, id string) (bool, error) {
	found := false
	err := r.update(ctx, topology, func(a *aggregate) error { _, found = a.OwnershipRetirements[id]; return nil })
	return found, err
}

func ValidateOwnershipTargetRaw(raw []byte, p controlplane.OwnershipTransferPlan) error {
	a, err := decodeAggregate(raw)
	if err != nil {
		return err
	}
	v, ok := a.OwnershipTargets[p.ID]
	if !ok || v.Aborted || v.DispositionHash != "" || evidenceHash(v.Plan) != evidenceHash(p) {
		return ErrConflict
	}
	return nil
}
func ApplyOwnershipAbort(sourceRaw, targetRaw, authorityRaw []byte, p controlplane.OwnershipTransferPlan) ([]byte, error) {
	if p.Validate() != nil {
		return nil, ErrInvalidRecord
	}
	source, err := decodeAggregate(sourceRaw)
	if err != nil {
		return nil, err
	}
	if _, retired := source.OwnershipRetirements[p.ID]; retired {
		return nil, ErrConflict
	}
	a, err := decodeAggregate(targetRaw)
	if err != nil {
		return nil, err
	}
	auth, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, err
	}
	digest, _ := p.Digest()
	if barrier, ok := auth.Maintenance[p.ID]; ok && (barrier.RequestDigest != digest || barrier.TopologyUID != p.Source.TopologyUID) {
		return nil, ErrConflict
	}
	v, ok := a.OwnershipTargets[p.ID]
	if !ok || evidenceHash(v.Plan) != evidenceHash(p) || v.CompletedHash != "" || v.DispositionHash != "" {
		return nil, ErrConflict
	}
	v.Aborted = true
	a.OwnershipTargets[p.ID] = v
	if err = validateAggregate(a, p.TargetTopology); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

func ValidateOwnershipAbortAuthority(raw []byte, p controlplane.OwnershipTransferPlan) error {
	if p.Validate() != nil {
		return ErrInvalidRecord
	}
	a, err := decodeExecutionAuthority(raw)
	if err != nil {
		return err
	}
	digest, _ := p.Digest()
	if barrier, ok := a.Maintenance[p.ID]; ok && (barrier.RequestDigest != digest || barrier.TopologyUID != p.Source.TopologyUID) {
		return ErrConflict
	}
	return nil
}
