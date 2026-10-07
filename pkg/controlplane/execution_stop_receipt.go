package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"time"
)

// SignedExecutionStopReceipt is produced only by the resource-host gate after
// it has proved that the grant cannot create further effects. Its signing key
// is distinct from both worker authentication and API issuer credentials.
type SignedExecutionStopReceipt struct {
	Grant      ExecutionGrant `json:"grant"`
	HostBootID string         `json:"host_boot_id"`
	ReceiptID  string         `json:"receipt_id"`
	StoppedAt  time.Time      `json:"stopped_at"`
	Signature  []byte         `json:"signature"`
}

func stopReceiptMessage(r SignedExecutionStopReceipt) ([]byte, error) {
	r.Signature = nil
	raw, err := json.Marshal(r)
	return append([]byte("sysbox.execution-stop.v1\x00"), raw...), err
}
func validStopReceipt(r SignedExecutionStopReceipt) bool {
	g := r.Grant
	return r.HostBootID != "" && r.ReceiptID != "" && !r.StoppedAt.IsZero() && g.Scope.TopologyUID != "" && g.AgentID != "" && g.SessionID != "" && g.ActionID != "" && g.CommandID != "" && g.Attempt > 0 && g.Epoch > 0 && g.Operation != "" && g.Digest != ""
}
func SignExecutionStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, bootID, receiptID string, now time.Time) (SignedExecutionStopReceipt, error) {
	r := SignedExecutionStopReceipt{Grant: g, HostBootID: bootID, ReceiptID: receiptID, StoppedAt: now.UTC()}
	if len(key) != ed25519.PrivateKeySize || !validStopReceipt(r) {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	raw, err := stopReceiptMessage(r)
	if err != nil {
		return SignedExecutionStopReceipt{}, err
	}
	r.Signature = ed25519.Sign(key, raw)
	return r, nil
}
func VerifyExecutionStopReceipt(key ed25519.PublicKey, r SignedExecutionStopReceipt) error {
	if len(key) != ed25519.PublicKeySize || !validStopReceipt(r) {
		return ErrInvalidExecutionGrant
	}
	raw, err := stopReceiptMessage(r)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, raw, r.Signature) {
		return ErrInvalidExecutionGrant
	}
	return nil
}
