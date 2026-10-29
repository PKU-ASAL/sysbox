package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// RetireOwnership must be called while the source authority storage is locked.
// It never calls providers and never treats lease expiry as process exclusion.
func (r *Repository) RetireOwnership(ctx context.Context, plan controlplane.OwnershipTransferPlan, authority []byte) (controlplane.OwnershipRetirementProof, error) {
	var proof controlplane.OwnershipRetirementProof
	err := r.update(ctx, plan.SourceTopology, func(a *aggregate) error {
		var err error
		proof, err = retireOwnership(a, plan, authority)
		return err
	})
	return proof, err
}
func retireOwnership(a *aggregate, plan controlplane.OwnershipTransferPlan, authorityRaw []byte) (controlplane.OwnershipRetirementProof, error) {
	var zero controlplane.OwnershipRetirementProof
	digest, err := plan.Digest()
	if err != nil {
		return zero, err
	}
	if prior, ok := a.OwnershipRetirements[plan.ID]; ok {
		priorDigest, err := prior.Plan.Digest()
		if err != nil || priorDigest != digest {
			return zero, ErrConflict
		}
		return prior, nil
	}
	authority, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return zero, err
	}
	barrier, ok := authority.Maintenance[plan.ID]
	if !ok || barrier.Released || barrier.TopologyUID != plan.Source.TopologyUID || barrier.RequestDigest != digest || a.State.TopologyUID != plan.Source.TopologyUID || a.Serial != plan.SourceSerial || gcPending(a) {
		return zero, ErrConflict
	}
	for _, g := range authority.Grants {
		if (g.Scope.TopologyUID == plan.Source.TopologyUID || g.Scope.TopologyUID == plan.Target.TopologyUID) && (g.State != controlplane.GrantStopped || g.StopReceipt == "") {
			return zero, ErrConflict
		}
	}
	for _, action := range a.Actions {
		if !runtime.ActionSettled(action) {
			return zero, ErrConflict
		}
	}
	addr, err := address.Parse(plan.SourceAddress)
	if err != nil {
		return zero, err
	}
	resource := a.State.FindResource(addr)
	if resource == nil || resource.Driver != plan.Provider || resource.ExternalID != plan.ExternalID || resource.OwnerUID != plan.Source.OwnerUID || resource.ResourceUID != plan.Source.ResourceUID || resource.Generation != plan.Source.Generation {
		return zero, ErrConflict
	}
	for _, other := range a.State.Resources {
		for _, dep := range other.Dependencies {
			if dep.Equal(addr) {
				return zero, fmt.Errorf("%w: retained dependency on transfer source", ErrConflict)
			}
		}
	}
	a.State.RemoveResource(addr)
	a.Serial++
	a.UpdatedAt = time.Now().UTC()

	stateRaw, err := json.Marshal(a.State)
	if err != nil {
		return zero, err
	}
	proof := controlplane.OwnershipRetirementProof{Plan: plan, PlanDigest: digest, SourceEpoch: barrier.Epoch, SourceStateDigest: evidenceHash(json.RawMessage(stateRaw)), RetiredAt: a.UpdatedAt}
	if a.OwnershipRetirements == nil {
		a.OwnershipRetirements = map[string]controlplane.OwnershipRetirementProof{}
	}
	a.OwnershipRetirements[plan.ID] = proof
	return proof, nil
}

// Retirement history survives topology incarnation resets. The original plan
// binds the retired owner; current state may have a new UID but may never
// reclaim the same external object or old resource identity.
func validateOwnershipRetirements(a *aggregate) error {

	for id, proof := range a.OwnershipRetirements {
		digest, err := proof.Plan.Digest()
		if err != nil || id != proof.Plan.ID || proof.PlanDigest != digest || proof.SourceEpoch == 0 || proof.SourceStateDigest == "" || proof.RetiredAt.IsZero() {
			return ErrCorruptRecord
		}
		for _, resource := range a.State.Resources {
			if resource.OwnerUID == proof.Plan.Source.OwnerUID && resource.ResourceUID == proof.Plan.Source.ResourceUID {
				return fmt.Errorf("%w: retired ownership cannot be restored", ErrConflict)
			}
			if resource.Driver == proof.Plan.Provider && resource.ExternalID == proof.Plan.ExternalID {
				return fmt.Errorf("%w: transferred external object cannot be reclaimed", ErrConflict)
			}
		}
	}
	return nil
}

// ApplyOwnershipRetirement is the SQL transaction adapter. Both authority and
// source aggregate must remain locked until the returned postimage is committed.
func ApplyOwnershipRetirement(raw, authority []byte, plan controlplane.OwnershipTransferPlan) ([]byte, controlplane.OwnershipRetirementProof, error) {
	var zero controlplane.OwnershipRetirementProof
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, zero, err
	}
	proof, err := retireOwnership(a, plan, authority)
	if err != nil {
		return nil, zero, err
	}
	if err = validateAggregate(a, plan.SourceTopology); err != nil {
		return nil, zero, err
	}
	next, err := json.Marshal(a)
	return next, proof, err
}

// InspectOwnershipSource is a non-mutating preflight. Canonical retirement
// revalidates the same conditions later under the authority transaction lock.
func (r *Repository) InspectOwnershipSource(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
	if p.Validate() != nil {
		return ErrInvalidRecord
	}
	return r.update(ctx, p.SourceTopology, func(a *aggregate) error {
		if prior, ok := a.OwnershipRetirements[p.ID]; ok {
			if evidenceHash(prior.Plan) != evidenceHash(p) {
				return ErrConflict
			}
			return nil
		}
		if a.State.TopologyUID != p.Source.TopologyUID || a.Serial != p.SourceSerial || gcPending(a) || ownershipPending(a) {
			return ErrConflict
		}
		addr, err := address.Parse(p.SourceAddress)
		if err != nil {
			return err
		}
		resource := a.State.FindResource(addr)
		if resource == nil || resource.ExternalID != p.ExternalID || resource.Driver != p.Provider || resource.OwnerUID != p.Source.OwnerUID || resource.ResourceUID != p.Source.ResourceUID || resource.Generation != p.Source.Generation {
			return ErrConflict
		}
		for _, other := range a.State.Resources {
			for _, dep := range other.Dependencies {
				if dep.Equal(addr) {
					return ErrConflict
				}
			}
		}
		return nil
	})
}
