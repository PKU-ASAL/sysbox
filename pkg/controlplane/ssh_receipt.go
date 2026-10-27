package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"time"
)

func SignSSHAccessStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, boot, id string, evidence json.RawMessage, now time.Time) (SignedExecutionStopReceipt, error) {
	if g.Operation != "ssh_access_apply" && g.Operation != "ssh_access_delete" {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	digest, err := executionEvidenceDigest(evidence)
	if err != nil {
		return SignedExecutionStopReceipt{}, err
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, HostBootID: boot, ReceiptID: id, StoppedAt: now.UTC(), Quiescence: "confirmed", EvidenceDigest: digest})
}

func VerifySSHAccessStopReceipt(key ed25519.PublicKey, receipt SignedExecutionStopReceipt, evidence json.RawMessage) error {
	if receipt.Grant.Operation != "ssh_access_apply" && receipt.Grant.Operation != "ssh_access_delete" {
		return ErrInvalidExecutionGrant
	}
	if err := VerifyExecutionStopReceipt(key, receipt); err != nil {
		return err
	}
	digest, err := executionEvidenceDigest(evidence)
	if err != nil || digest != receipt.EvidenceDigest {
		return ErrInvalidExecutionGrant
	}
	return nil
}
