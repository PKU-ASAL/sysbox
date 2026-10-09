package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
)

// SignedExecutionCancellation grants only the right to stop one issued epoch.
// It does not expire: replay remains safe because task, scope, boot and epoch
// are immutable, and no start/renew path accepts this signature domain.
type SignedExecutionCancellation struct {
	Grant     ExecutionGrant `json:"grant"`
	Signature []byte         `json:"signature"`
}

func validExecutionCancellation(g ExecutionGrant) bool {
	return g.State == GrantRevoked && g.StopReceipt == "" && g.Operation == "guest_task" && g.Scope.TopologyUID != "" && g.Scope.ResourceUID != "" && g.Scope.Generation > 0 && g.AgentID != "" && g.SessionID != "" && g.CommandID != "" && g.ActionID != "" && g.TargetBootID != "" && g.Digest != "" && g.Attempt > 0 && g.Epoch > 0 && !g.ExpiresAt.IsZero()
}
func cancellationMessage(s SignedExecutionCancellation) ([]byte, error) {
	s.Signature = nil
	raw, err := json.Marshal(s)
	return append([]byte("sysbox.execution-cancel.v1\x00"), raw...), err
}
func SignExecutionCancellation(key ed25519.PrivateKey, g ExecutionGrant) (SignedExecutionCancellation, error) {
	if len(key) != ed25519.PrivateKeySize || !validExecutionCancellation(g) {
		return SignedExecutionCancellation{}, ErrInvalidExecutionGrant
	}
	s := SignedExecutionCancellation{Grant: g}
	raw, err := cancellationMessage(s)
	if err != nil {
		return s, err
	}
	s.Signature = ed25519.Sign(key, raw)
	return s, nil
}
func VerifyExecutionCancellation(key ed25519.PublicKey, s SignedExecutionCancellation) error {
	if len(key) != ed25519.PublicKeySize || !validExecutionCancellation(s.Grant) {
		return ErrInvalidExecutionGrant
	}
	raw, err := cancellationMessage(s)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, raw, s.Signature) {
		return ErrInvalidExecutionGrant
	}
	return nil
}
