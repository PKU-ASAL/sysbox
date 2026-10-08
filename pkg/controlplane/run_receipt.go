package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"time"
)

// SignRunStopReceipt binds the durable host outcome to the original execution
// authority. A generic quiescence receipt cannot authenticate a Run result.
func SignRunStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, boot, id string, evidence json.RawMessage, now time.Time) (SignedExecutionStopReceipt, error) {
	if g.Operation != "run_apply" && g.Operation != "run_destroy" {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	digest, err := executionEvidenceDigest(evidence)
	if err != nil {
		return SignedExecutionStopReceipt{}, err
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, HostBootID: boot, ReceiptID: id, StoppedAt: now.UTC(), Quiescence: "confirmed", EvidenceDigest: digest})
}

func VerifyRunStopReceipt(key ed25519.PublicKey, r SignedExecutionStopReceipt, evidence json.RawMessage) error {
	if r.Grant.Operation != "run_apply" && r.Grant.Operation != "run_destroy" {
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
