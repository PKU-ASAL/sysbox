package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"
)

const ExecutionGrantMaxTTL = 5 * time.Minute

// Deployments must keep authority and resource-host/guest clocks within this
// bound. Subtract it from the local monotonic budget so a slow clock cannot
// extend effects beyond the authority deadline.
const ExecutionGrantMaxClockSkew = time.Second

var ErrInvalidExecutionGrant = errors.New("invalid execution grant")

// SignedExecutionGrant binds the authorization to a freshly observed host or
// guest boot. Only the control plane holds the private key. A worker never
// receives authority to extend deadlines or change request digests.
type SignedExecutionGrant struct {
	Grant     ExecutionGrant `json:"grant"`
	BootID    string         `json:"boot_id"`
	IssuedAt  time.Time      `json:"issued_at"`
	Signature []byte         `json:"signature"`
}

func grantSigningMessage(s SignedExecutionGrant) ([]byte, error) {
	s.Signature = nil
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return append([]byte("sysbox.execution-grant.v1\x00"), raw...), nil
}

func SignExecutionGrant(key ed25519.PrivateKey, g ExecutionGrant, bootID string, now time.Time) (SignedExecutionGrant, error) {
	s := SignedExecutionGrant{Grant: g, BootID: bootID, IssuedAt: now.UTC()}
	if len(key) != ed25519.PrivateKeySize || !validSignedGrant(s, bootID, now) {
		return SignedExecutionGrant{}, ErrInvalidExecutionGrant
	}
	raw, err := grantSigningMessage(s)
	if err != nil {
		return SignedExecutionGrant{}, err
	}
	s.Signature = ed25519.Sign(key, raw)
	return s, nil
}
func validSignedGrant(s SignedExecutionGrant, bootID string, now time.Time) bool {
	g := s.Grant
	_, err := ExecutionGrantBudget(s, now)
	return err == nil && bootID != "" && s.BootID == bootID && g.State == GrantActive && g.StopReceipt == "" && g.Scope.TopologyUID != "" && (g.Scope.ResourceUID == "" && g.Scope.Generation == 0 || g.Scope.ResourceUID != "" && g.Scope.Generation > 0) && g.AgentID != "" && g.SessionID != "" && g.ActionID != "" && g.CommandID != "" && g.Attempt > 0 && g.Epoch > 0 && g.Operation != "" && g.Digest != ""
}

// ExecutionGrantBudget must be captured once into a monotonic deadline on
// verification/renewal. Recomputing it from wall time could resurrect a lease
// after a clock step. This does not verify the signature; call Verify first.
func ExecutionGrantBudget(s SignedExecutionGrant, now time.Time) (time.Duration, error) {
	ttl := s.Grant.ExpiresAt.Sub(s.IssuedAt)
	if s.IssuedAt.IsZero() || s.IssuedAt.After(now.Add(ExecutionGrantMaxClockSkew)) || ttl <= ExecutionGrantMaxClockSkew || ttl > ExecutionGrantMaxTTL {
		return 0, ErrInvalidExecutionGrant
	}
	budget := s.Grant.ExpiresAt.Sub(now) - ExecutionGrantMaxClockSkew
	if budget > ttl {
		budget = ttl
	}
	if budget <= 0 {
		return 0, ErrInvalidExecutionGrant
	}
	return budget, nil
}

func VerifyExecutionGrant(key ed25519.PublicKey, s SignedExecutionGrant, bootID string, now time.Time) error {
	if len(key) != ed25519.PublicKeySize || !validSignedGrant(s, bootID, now) {
		return ErrInvalidExecutionGrant
	}
	raw, err := grantSigningMessage(s)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, raw, s.Signature) {
		return ErrInvalidExecutionGrant
	}
	return nil
}
