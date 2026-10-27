package controlplane

import (
	"crypto/ed25519"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/substrate"
)

// SSHGrantDerivation binds a trusted host's selected payload and target to the
// exact parent authorization. Agent credentials alone do not prove this choice.
type SSHGrantDerivation struct {
	Parent        SignedExecutionGrant        `json:"parent"`
	Request       substrate.SSHAccessMutation `json:"request"`
	ResourceUID   string                      `json:"resource_uid"`
	Generation    int64                       `json:"generation"`
	GuestBootID   string                      `json:"guest_boot_id"`
	HostSignature []byte                      `json:"host_signature,omitempty"`
}

func (r SSHGrantDerivation) signingBytes() ([]byte, error) {
	if r.Parent.Grant.AgentID == "" || r.ResourceUID == "" || r.Generation < 1 || r.GuestBootID == "" || r.Request.Validate() != nil {
		return nil, ErrInvalidExecutionGrant
	}
	r.HostSignature = nil
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append([]byte("sysbox/ssh-grant-derivation/v1\x00"), raw...), nil
}

func SignSSHGrantDerivation(key ed25519.PrivateKey, r SSHGrantDerivation) (SSHGrantDerivation, error) {
	if len(key) != ed25519.PrivateKeySize {
		return SSHGrantDerivation{}, ErrInvalidExecutionGrant
	}
	raw, err := r.signingBytes()
	if err != nil {
		return SSHGrantDerivation{}, err
	}
	r.HostSignature = ed25519.Sign(key, raw)
	return r, nil
}

// VerifySSHGrantDerivation verifies origin and binding only. The caller must
// still validate the issuer signature, current parent authority and source lease.
func VerifySSHGrantDerivation(key ed25519.PublicKey, r SSHGrantDerivation) error {
	raw, err := r.signingBytes()
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, r.HostSignature) {
		return ErrInvalidExecutionGrant
	}
	return nil
}
