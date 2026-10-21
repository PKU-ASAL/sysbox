package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// GCReservation freezes the canonical reference set before dispatch. It has
// no expiry: losing a worker/lease cannot establish that deletion stopped.
// ID is the durable command ID. Completed entries retain exact replay identity.
type GCReservation struct {
	AdmissionDigest string                   `json:"admission_digest,omitempty"`
	DispositionHash string                   `json:"disposition_hash,omitempty"`
	ID              string                   `json:"id"`
	AgentID         string                   `json:"agent_id"`
	Candidate       controlplane.GCCandidate `json:"candidate"`
	ReceiptHash     string                   `json:"receipt_hash,omitempty"`
}

func gcPending(a *aggregate) bool {
	for _, r := range a.GC {
		if r.Pending() {
			return true
		}
	}
	return false
}

func validateGCReservations(a *aggregate) error {
	if a.Format == GCAdmissionFormatVersion && (a.GCReferenceFormat < FormatVersion || a.GCReferenceFormat >= GCAdmissionFormatVersion) {
		return ErrCorruptRecord
	}
	if a.Format != GCAdmissionFormatVersion && a.Format != AdoptionRecoveryFormatVersion && a.Format != LegacyAdoptionRecoveryFormatVersion && a.GCReferenceFormat != 0 {
		return ErrCorruptRecord
	}
	pending := 0
	groups := map[string]bool{}
	for id, r := range a.GC {
		if r.AdmissionDigest != "" && (len(r.AdmissionDigest) != 64 || a.Format != GCAdmissionFormatVersion && a.Format != AdoptionRecoveryFormatVersion && a.Format != LegacyAdoptionRecoveryFormatVersion) {
			return ErrCorruptRecord
		}
		if r.DispositionHash != "" && a.Format != GCDispositionFormatVersion && a.Format != OwnershipRetirementFormatVersion && a.Format != OwnershipTargetFormatVersion && a.Format != GCAdmissionFormatVersion && a.Format != AdoptionRecoveryFormatVersion && a.Format != LegacyAdoptionRecoveryFormatVersion {
			return ErrCorruptRecord
		}
		if id == "" || id != r.ID || r.AgentID == "" || r.Candidate.Validate() != nil || r.Candidate.TopologyUID != a.State.TopologyUID {
			return ErrCorruptRecord
		}
		if r.Pending() {
			pending++
			groups[batchPrefix(r.ID)] = true
		} else if (len(r.ReceiptHash) != 64 && len(r.DispositionHash) != 64) || (r.ReceiptHash != "" && r.DispositionHash != "") {
			return ErrCorruptRecord
		}
	}
	if pending > MaxGCBatch || (pending > 1 && (len(groups) != 1 || groups[""])) {
		return ErrCorruptRecord
	}
	return nil
}

func (r *Repository) ReserveGC(ctx context.Context, topology string, request GCReservation) error {
	if !topologyName.MatchString(request.ID) || request.AgentID == "" || (!request.Pending() || request.AdmissionDigest != "") || request.Candidate.Validate() != nil {
		return ErrInvalidRecord
	}
	return r.updateGC(ctx, topology, true, func(a *aggregate) error { return reserveGC(a, request) })
}

func reserveGC(a *aggregate, request GCReservation) error {
	if !topologyName.MatchString(request.ID) || request.AgentID == "" || (!request.Pending() || request.AdmissionDigest != "") || request.Candidate.Validate() != nil {
		return ErrInvalidRecord
	}

	if old, ok := a.GC[request.ID]; ok {
		if old.ID != request.ID || old.AgentID != request.AgentID || old.Candidate != request.Candidate {
			return ErrConflict
		}
		return nil
	}
	if gcPending(a) {
		return ErrConflict
	}
	c := request.Candidate
	command := controlplane.AgentCommand{ID: request.ID, AgentID: request.AgentID, Type: "gc_delete", TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: c.Generation, GCKind: c.Kind, GCExternalID: c.ExternalID, GCInventoryDigest: c.InventoryDigest}
	if err := validateGCReferences(a, command); err != nil {
		return err
	}
	a.GC[request.ID] = request
	return nil
}

func (r *Repository) LoadGCReservation(ctx context.Context, topology, id string) (GCReservation, error) {
	var out GCReservation
	err := r.update(ctx, topology, func(a *aggregate) error {
		var ok bool
		out, ok = a.GC[id]
		if !ok {
			return ErrNotFound
		}
		return nil
	})
	return out, err
}

func checkGCCommand(a *aggregate, command controlplane.AgentCommand) error {
	next := nextGC(a)
	if command.Type != "gc_delete" {
		if next != "" {
			return ErrConflict
		}
		return nil
	}
	r, ok := a.GC[command.ID]
	if !ok || !r.Pending() || command.ID != next {
		return ErrConflict
	}
	c := r.Candidate
	if command.ActionID != r.ID || command.AgentID != r.AgentID || command.TopologyUID != c.TopologyUID || command.ResourceUID != c.ResourceUID || command.Generation != c.Generation || command.GCKind != c.Kind || command.GCExternalID != c.ExternalID || command.GCInventoryDigest != c.InventoryDigest {
		return ErrConflict
	}
	return nil
}

// CompleteGC accepts only an enrolled host's exact absence receipt. Authority
// validation/retirement belongs to the API caller and precedes this transition.
// A lost response can repeat this exact completion, never change its evidence.
func (r *Repository) CompleteGC(ctx context.Context, topology string, receipt controlplane.SignedExecutionStopReceipt, key ed25519.PublicKey) error {
	return r.updateGC(ctx, topology, true, func(a *aggregate) error {
		stored, ok := a.GC[receipt.Grant.CommandID]
		if !ok {
			return ErrNotFound
		}
		if stored.DispositionHash != "" || stored.AgentID != receipt.Grant.AgentID || receipt.Grant.ActionID != stored.ID {
			return ErrConflict
		}
		if err := controlplane.VerifyGCDeletionReceipt(key, receipt, stored.Candidate); err != nil {
			return err
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(raw))
		if stored.ReceiptHash != "" && stored.ReceiptHash != hash {
			return ErrConflict
		}
		stored.ReceiptHash = hash
		a.GC[stored.ID] = stored
		return nil
	})
}

// ApplyGCReservation runs under the authority and aggregate transaction locks.
// The caller additionally checks all API activity registries under that boundary.
func ApplyGCReservation(raw, authorityRaw []byte, topology string, request GCReservation) ([]byte, error) {
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, err
	}
	if err := validateAggregate(a, topology); err != nil {
		return nil, err
	}
	if prior, ok := a.GC[request.ID]; ok {
		if prior.AgentID != request.AgentID || prior.Candidate != request.Candidate || !request.Pending() {
			return nil, ErrConflict
		}
		return append([]byte(nil), raw...), nil
	}
	authority, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, err
	}
	if authority.maintenanceBlocks(a.State.TopologyUID) {
		return nil, ErrConflict
	}
	for _, g := range authority.Grants {
		if g.Scope.TopologyUID == a.State.TopologyUID && (g.State != controlplane.GrantStopped || g.StopReceipt == "") {
			return nil, ErrConflict
		}
	}
	if err := reserveGC(a, request); err != nil {
		return nil, err
	}
	if err := validateAggregate(a, topology); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

func (r *Repository) ReserveGCWithAuthority(ctx context.Context, topology string, request GCReservation, authority []byte) error {
	return r.updateGC(ctx, topology, true, func(a *aggregate) error {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		next, err := ApplyGCReservation(raw, authority, topology, request)
		if err != nil {
			return err
		}
		decoded, err := decodeAggregate(next)
		if err != nil {
			return err
		}
		*a = *decoded
		return nil
	})
}

func ReadGCReservation(raw []byte, id string) (GCReservation, error) {
	a, err := decodeAggregate(raw)
	if err != nil {
		return GCReservation{}, err
	}
	reservation, ok := a.GC[id]
	if !ok {
		return GCReservation{}, ErrNotFound
	}
	return reservation, nil
}

func (r *Repository) ListGCReservations(ctx context.Context, topology string) ([]GCReservation, error) {
	out := []GCReservation{}
	err := r.update(ctx, topology, func(a *aggregate) error {
		for _, reservation := range a.GC {
			out = append(out, reservation)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}
