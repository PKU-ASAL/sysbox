package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
)

// GuestGrantDerivation binds a trusted host's selected task to its parent and
// exact guest incarnation. The task digest covers the complete execution payload.
type GuestGrantDerivation struct {
	Parent        SignedExecutionGrant `json:"parent"`
	TaskID        string               `json:"task_id"`
	TaskDigest    string               `json:"task_digest"`
	ResourceUID   string               `json:"resource_uid"`
	Generation    int64                `json:"generation"`
	GuestBootID   string               `json:"guest_boot_id"`
	HostSignature []byte               `json:"host_signature,omitempty"`
}

func (r GuestGrantDerivation) signingBytes() ([]byte, error) {
	if r.Parent.Grant.AgentID == "" || r.TaskID == "" || r.TaskDigest == "" || r.ResourceUID == "" || r.Generation < 1 || r.GuestBootID == "" {
		return nil, ErrInvalidExecutionGrant
	}
	r.HostSignature = nil
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append([]byte("sysbox/guest-grant-derivation/v1\x00"), raw...), nil
}

func SignGuestGrantDerivation(key ed25519.PrivateKey, r GuestGrantDerivation) (GuestGrantDerivation, error) {
	if len(key) != ed25519.PrivateKeySize {
		return GuestGrantDerivation{}, ErrInvalidExecutionGrant
	}
	raw, err := r.signingBytes()
	if err != nil {
		return GuestGrantDerivation{}, err
	}
	r.HostSignature = ed25519.Sign(key, raw)
	return r, nil
}

// VerifyGuestGrantDerivation verifies host origin only. Issuer, live parent and
// source lease validation remain mandatory at the authority boundary.
func VerifyGuestGrantDerivation(key ed25519.PublicKey, r GuestGrantDerivation) error {
	raw, err := r.signingBytes()
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, r.HostSignature) {
		return ErrInvalidExecutionGrant
	}
	return nil
}
