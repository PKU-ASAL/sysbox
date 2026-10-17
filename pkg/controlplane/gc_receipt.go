package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"time"
)

func gcDeletionEvidence(candidate GCCandidate) (json.RawMessage, error) {
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Candidate GCCandidate `json:"candidate"`
		Absent    bool        `json:"absent"`
	}{candidate, true})
}

// SignGCDeletionReceipt is called only after exact provider absence verification.
// A generic host-stop receipt cannot assert that the object was deleted.
func SignGCDeletionReceipt(key ed25519.PrivateKey, g ExecutionGrant, boot, id string, candidate GCCandidate, now time.Time) (SignedExecutionStopReceipt, error) {
	digest, err := candidate.Digest()
	if err != nil || g.Operation != "gc_delete" || g.Digest != digest || g.Scope != (ExecutionScope{TopologyUID: candidate.TopologyUID, ResourceUID: candidate.ResourceUID, Generation: candidate.Generation}) || g.TargetBootID != boot {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	raw, err := gcDeletionEvidence(candidate)
	if err != nil {
		return SignedExecutionStopReceipt{}, err
	}
	evidence, err := executionEvidenceDigest(raw)
	if err != nil {
		return SignedExecutionStopReceipt{}, err
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, HostBootID: boot, ReceiptID: id, StoppedAt: now.UTC(), Quiescence: "confirmed", EvidenceDigest: evidence})
}

func VerifyGCDeletionReceipt(key ed25519.PublicKey, receipt SignedExecutionStopReceipt, candidate GCCandidate) error {
	digest, err := candidate.Digest()
	g := receipt.Grant
	if err != nil || g.Operation != "gc_delete" || g.Digest != digest || g.Scope != (ExecutionScope{TopologyUID: candidate.TopologyUID, ResourceUID: candidate.ResourceUID, Generation: candidate.Generation}) || g.TargetBootID != receipt.HostBootID {
		return ErrInvalidExecutionGrant
	}
	if err := VerifyExecutionStopReceipt(key, receipt); err != nil {
		return err
	}
	raw, err := gcDeletionEvidence(candidate)
	if err != nil {
		return err
	}
	evidence, err := executionEvidenceDigest(raw)
	if err != nil || evidence != receipt.EvidenceDigest {
		return ErrInvalidExecutionGrant
	}
	return nil
}
