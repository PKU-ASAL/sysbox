package controlplane

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

func executionEvidenceDigest(raw json.RawMessage) (string, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", ErrInvalidExecutionGrant
	}
	if compact.Len() == 0 || compact.Bytes()[0] != '{' {
		return "", ErrInvalidExecutionGrant
	}
	sum := sha256.Sum256(compact.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// SignAdoptionStopReceipt binds the exact provider codec output as well as
// the original grant. Transport whitespace compaction cannot change the proof.
func SignAdoptionStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, boot, id string, evidence json.RawMessage, now time.Time) (SignedExecutionStopReceipt, error) {
	if g.Operation != "adoption_apply" && g.Operation != "adoption_inspect" {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	digest, err := executionEvidenceDigest(evidence)
	if err != nil {
		return SignedExecutionStopReceipt{}, err
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, HostBootID: boot, ReceiptID: id, StoppedAt: now.UTC(), Quiescence: "confirmed", EvidenceDigest: digest})
}

func VerifyAdoptionStopReceipt(key ed25519.PublicKey, r SignedExecutionStopReceipt, evidence json.RawMessage) error {
	if r.Grant.Operation != "adoption_apply" && r.Grant.Operation != "adoption_inspect" {
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
