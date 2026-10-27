package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
)

// SignedExecutionCancellation grants only the right to stop one issued epoch.
// It does not expire: replay remains safe because task, scope, boot and epoch
// are immutable, and no start/renew path accepts this signature domain.
type SignedExecutionCancellation struct {
	Grant     ExecutionGrant `json:"grant"`
	RunID     string         `json:"run_id,omitempty"`
	Topology  string         `json:"topology,omitempty"`
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
	return signExecutionCancellation(key, SignedExecutionCancellation{Grant: g})
}

// SignRunExecutionCancellation binds trusted recovery routing to a child grant.
// The issuer must first verify the immutable source command and parent grant.
func SignRunExecutionCancellation(key ed25519.PrivateKey, g ExecutionGrant, runID, topology string) (SignedExecutionCancellation, error) {
	if runID == "" || topology == "" || g.ParentEpoch == 0 {
		return SignedExecutionCancellation{}, ErrInvalidExecutionGrant
	}
	return signExecutionCancellation(key, SignedExecutionCancellation{Grant: g, RunID: runID, Topology: topology})
}

func validCancellationSource(s SignedExecutionCancellation) bool {
	if s.RunID == "" && s.Topology == "" {
		return true
	}
	return s.Grant.ParentEpoch != 0 && s.RunID != "" && s.Topology != "" && s.RunID != "." && s.RunID != ".." && s.Topology != "." && s.Topology != ".." && !strings.ContainsAny(s.RunID+s.Topology, "/\\\x00")
}

func signExecutionCancellation(key ed25519.PrivateKey, s SignedExecutionCancellation) (SignedExecutionCancellation, error) {
	if len(key) != ed25519.PrivateKeySize || !validExecutionCancellation(s.Grant) || !validCancellationSource(s) {
		return SignedExecutionCancellation{}, ErrInvalidExecutionGrant
	}
	raw, err := cancellationMessage(s)
	if err != nil {
		return s, err
	}
	s.Signature = ed25519.Sign(key, raw)
	return s, nil
}
func VerifyExecutionCancellation(key ed25519.PublicKey, s SignedExecutionCancellation) error {
	if len(key) != ed25519.PublicKeySize || !validExecutionCancellation(s.Grant) || !validCancellationSource(s) {
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
