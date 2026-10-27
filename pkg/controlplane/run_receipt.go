package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"time"
)

// RunHostCompletionProof retains the exact signed result bytes, independently
// of subsequent normalization of the canonical Run. Evidence must not contain
// another completion proof or transient input secrets.
type RunHostCompletionProof struct {
	Receipt  SignedExecutionStopReceipt `json:"receipt"`
	Evidence []byte                     `json:"evidence"` // base64 preserves signed bytes through JSONB
}

// SignRunStopReceipt binds the durable host outcome to the original execution
// authority. A generic quiescence receipt cannot authenticate a Run result.
func SignRunStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, boot, id string, evidence json.RawMessage, now time.Time) (SignedExecutionStopReceipt, error) {
	if !IsRunExecutionOperation(g.Operation) {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	digest, err := executionEvidenceDigest(evidence)
	if err != nil {
		return SignedExecutionStopReceipt{}, err
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, HostBootID: boot, ReceiptID: id, StoppedAt: now.UTC(), Quiescence: "confirmed", EvidenceDigest: digest})
}

func VerifyRunStopReceipt(key ed25519.PublicKey, r SignedExecutionStopReceipt, evidence json.RawMessage) error {
	if !IsRunExecutionOperation(r.Grant.Operation) {
		return ErrInvalidExecutionGrant
	}
	if r.FileResult != nil || r.GuestResult != nil || r.ObservedGuestBootID != "" || r.GuestBootID != "" || r.InstallID != "" || r.InstallDigest != "" || r.TaskID != "" || r.TaskDigest != "" {
		return ErrInvalidExecutionGrant
	}
	if err := VerifyExecutionStopReceipt(key, r); err != nil {
		return err
	}
	digest, err := executionEvidenceDigest(evidence)
	if err != nil || digest != r.EvidenceDigest {
		return ErrInvalidExecutionGrant
	}
	return nil
}
