package persistence

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/runtime"
	"reflect"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

const LegacyAdoptionRecoveryFormatVersion = 13
const AdoptionRecoveryFormatVersion = 14

type AdoptionRecoveryRecord struct {
	Actions     map[string]runtime.ActionRecord       `json:"actions,omitempty"`
	Proof       controlplane.AdoptionDispositionProof `json:"proof"`
	HostKey     []byte                                `json:"host_key"`
	TransferKey []byte                                `json:"transfer_key,omitempty"`
	CommittedAt time.Time                             `json:"committed_at"`
}

// ApplyFencedAdoption requires a prepared immutable decision. The caller holds
// authority and lifecycle locks until the returned aggregate is committed.
func ApplyFencedAdoption(raw, authorityRaw []byte, p controlplane.AdoptionDispositionProof, hostKey, transferKey ed25519.PublicKey) ([]byte, error) {
	if err := controlplane.VerifyAdoptionDisposition(hostKey, p); err != nil {
		return nil, err
	}
	auth, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, err
	}
	f, ok := auth.Fences[p.Request.ID]
	hash := AdoptionDispositionHash(p)
	if !ok || f.AdoptionDisposition == nil || AdoptionDispositionHash(f.AdoptionDisposition.Proof) != hash {
		return nil, ErrConflict
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, err
	}
	if err = validateAggregate(a, p.Adoption.Topology); err != nil {
		return nil, err
	}
	if prior, ok := a.AdoptionRecoveries[p.Request.ID]; ok {
		if AdoptionDispositionHash(prior.Proof) != hash {
			return nil, ErrConflict
		}
		return append([]byte(nil), raw...), nil
	}
	if f.released() {
		return nil, ErrConflict
	}
	req := p.Adoption
	addr, err := address.Parse(req.Address)
	if err != nil {
		return nil, err
	}
	if a.State.TopologyUID != req.TopologyUID || a.Serial != req.ExpectedSerial || a.State.FindResource(addr) != nil || gcPending(a) {
		return nil, ErrConflict
	}
	archived := map[string]runtime.ActionRecord{}
	for id, action := range a.Actions {
		if action.RunID != req.ID {
			continue
		}
		if action.DispositionID != "" || action.Status == runtime.ActionSucceeded || action.Action != controlplane.PlanActionAdopt || action.Topology != req.Topology || !action.Resource.Equal(addr) || action.OwnerUID != req.TopologyUID || action.ResourceUID != req.ResourceUID || action.Generation != req.Generation || action.StateSerial != req.ExpectedSerial || action.Candidate == nil || action.Candidate.ExternalID != req.ExternalID || action.Candidate.Driver != req.Provider {
			return nil, ErrConflict
		}
		archived[id] = action
	}

	if req.Transfer != nil {
		if controlplane.VerifyOwnershipRetirement(transferKey, *req.Transfer) != nil {
			return nil, ErrConflict
		}
		target, ok := a.OwnershipTargets[req.ID]
		if !ok || target.Aborted || target.CompletedHash != "" || target.DispositionHash != "" || evidenceHash(target.Plan) != evidenceHash(req.Transfer.Plan) {
			return nil, ErrConflict
		}
		target.DispositionHash = hash
		a.OwnershipTargets[req.ID] = target
	} else if ownershipPending(a) {
		return nil, ErrConflict
	}
	if p.Disposition() == "recover-owned" {
		a.State.AddResource(state.Resource{Address: addr, ResourceType: addr.Type, Driver: req.Provider, ExternalID: req.ExternalID, OwnerUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation, Private: append(json.RawMessage(nil), p.Private...), Status: state.ResourceUnknown, CreatedAt: p.ObservedAt, UpdatedAt: p.ObservedAt})
	}
	if a.AdoptionRecoveries == nil {
		a.AdoptionRecoveries = map[string]AdoptionRecoveryRecord{}
	}
	a.AdoptionRecoveries[p.Request.ID] = AdoptionRecoveryRecord{Actions: archived, Proof: p, HostKey: append([]byte(nil), hostKey...), TransferKey: append([]byte(nil), transferKey...), CommittedAt: time.Now().UTC()}
	for id, action := range archived {
		action.DispositionID = p.Request.ID
		a.Actions[id] = action
	}
	a.Format = AdoptionRecoveryFormatVersion
	a.Serial++
	a.UpdatedAt = time.Now().UTC()
	if err = validateAggregate(a, req.Topology); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}
func (r *Repository) CommitFencedAdoption(ctx context.Context, p controlplane.AdoptionDispositionProof, hostKey, transferKey ed25519.PublicKey, authority []byte) error {
	return r.updateOwnership(ctx, p.Adoption.Topology, func(a *aggregate) error {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		next, err := ApplyFencedAdoption(raw, authority, p, hostKey, transferKey)
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
func validateAdoptionRecoveries(a *aggregate) error {
	if len(a.AdoptionRecoveries) > 0 && a.Format != AdoptionRecoveryFormatVersion && a.Format != LegacyAdoptionRecoveryFormatVersion {
		return ErrCorruptRecord
	}
	for id, r := range a.AdoptionRecoveries {
		if (len(r.Actions) > 0 || r.Proof.Disposition() != "recover-owned") && a.Format != AdoptionRecoveryFormatVersion {
			return ErrCorruptRecord
		}
		for actionID, old := range r.Actions {
			req := r.Proof.Adoption
			addr, err := address.Parse(req.Address)
			if err != nil || validateAction(old) != nil || old.Revision == 0 || old.ID != actionID || old.DispositionID != "" || old.Action != controlplane.PlanActionAdopt || (old.Status != runtime.ActionIntentCommitted && old.Status != runtime.ActionUncertain && old.Status != runtime.ActionBlocked) || old.RunID != req.ID || old.Topology != req.Topology || !old.Resource.Equal(addr) || old.OwnerUID != req.TopologyUID || old.ResourceUID != req.ResourceUID || old.Generation != req.Generation || old.StateSerial != req.ExpectedSerial || old.Candidate == nil || old.Candidate.ExternalID != req.ExternalID || old.Candidate.Driver != req.Provider {
				return ErrCorruptRecord
			}
			if current, ok := a.Actions[actionID]; ok {
				if current.DispositionID != id {
					return ErrCorruptRecord
				}
				current.DispositionID = ""
				if !reflect.DeepEqual(old, current) {
					return ErrCorruptRecord
				}
			}
		}
		if id != r.Proof.Request.ID || controlplane.VerifyAdoptionDisposition(r.HostKey, r.Proof) != nil || r.CommittedAt.Before(r.Proof.ObservedAt) {
			return ErrCorruptRecord
		}
		if r.Proof.Adoption.Transfer != nil && controlplane.VerifyOwnershipRetirement(r.TransferKey, *r.Proof.Adoption.Transfer) != nil {
			return ErrCorruptRecord
		}
	}
	for id, action := range a.Actions {
		if action.DispositionID == "" {
			continue
		}
		r, ok := a.AdoptionRecoveries[action.DispositionID]
		if !ok {
			return ErrCorruptRecord
		}
		if _, ok = r.Actions[id]; !ok {
			return ErrCorruptRecord
		}
	}
	return nil
}
