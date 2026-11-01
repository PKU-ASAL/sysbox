package persistence

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// ApplyHostAdoptionState serializes normal completion with fencing decisions.
// Callers hold authority and aggregate exclusion through commit.
func ApplyHostAdoptionState(raw, authorityRaw []byte, op controlplane.NodeOperation, evidence json.RawMessage, key ed25519.PublicKey) ([]byte, error) {
	if op.Adoption == nil {
		return nil, ErrConflict
	}
	req := op.Adoption
	auth, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, err
	}
	for _, f := range auth.Fences {
		if f.Request.Grant.Operation == "adoption_apply" && f.Request.Grant.CommandID == op.CommandID && f.Request.Grant.ActionID == op.ID {
			return nil, ErrConflict
		}
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, err
	}
	addr, err := address.Parse(req.Address)
	if err != nil {
		return nil, err
	}
	resource := state.Resource{Address: addr, ResourceType: addr.Type, Driver: req.Provider, ExternalID: req.ExternalID, OwnerUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation, Private: append(json.RawMessage(nil), evidence...), Status: state.ResourcePresent, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	stable := resource
	stable.CreatedAt, stable.UpdatedAt = time.Time{}, time.Time{}
	if req.Transfer != nil {
		if controlplane.VerifyOwnershipRetirement(key, *req.Transfer) != nil {
			return nil, ErrConflict
		}
		completion := evidenceHash(struct {
			Proof    controlplane.OwnershipRetirementProof
			Resource state.Resource
		}{*req.Transfer, stable})
		err = commitOwnershipTarget(a, req.Transfer.Plan, addr, resource, completion)
	} else {
		identity := substrate.OperationIdentity{ActionID: op.ID, OwnerUID: req.TopologyUID, TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}
		digest := evidenceHash(struct {
			Run, Topology string
			Identity      substrate.OperationIdentity
			Evidence      substrate.AdoptionEvidence
			Resource      state.Resource
		}{op.ID, req.Topology, identity, req.Evidence, stable})
		action := runtime.ActionRecord{ID: "adopt-" + digest, Topology: req.Topology, RunID: op.ID, Resource: addr, Action: model.PlanActionAdopt, Status: runtime.ActionIntentCommitted, DesiredDigest: digest, OwnerUID: identity.OwnerUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation, StateSerial: req.ExpectedSerial, ExpectedSerial: req.ExpectedSerial, Candidate: &resource}
		if err = validateAction(action); err == nil {
			err = commitAdoptionEvidence(a, action, addr, identity, req.ExpectedSerial, digest)
		}
	}
	if err != nil {
		return nil, err
	}
	original, decodeErr := decodeAggregate(raw)
	if decodeErr != nil {
		return nil, decodeErr
	}
	if (gcPending(original) || req.Transfer == nil && ownershipPending(original)) && a.Serial != original.Serial {
		return nil, ErrConflict
	}
	if err = validateAggregate(a, req.Topology); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

func (r *Repository) ApplyAdoptionWithAuthority(ctx context.Context, topology string, authority []byte, apply func([]byte, []byte) ([]byte, error)) error {
	return r.updateOwnership(ctx, topology, func(a *aggregate) error {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		next, err := apply(raw, authority)
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
