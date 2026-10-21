package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"
)

// GCDispositionProof closes execution uncertainty, not historical outcome.
// Both present and absent observations leave the original operation unknown.
// They authorize retiring its dispatch only after independent host fencing.
type GCDispositionProof struct {
	Request    HostFenceRequest `json:"request"`
	Fence      HostFenceReceipt `json:"fence"`
	Candidate  GCCandidate      `json:"candidate"`
	HostBootID string           `json:"host_boot_id"`
	Presence   string           `json:"presence"`
	ObservedAt time.Time        `json:"observed_at"`
	Signature  []byte           `json:"signature"`
}

func (p GCDispositionProof) validate() error {
	g := p.Request.Grant
	digest, err := p.Candidate.Digest()
	if err != nil {
		return err
	}
	if g.Operation != "gc_delete" || g.Digest != digest || g.Scope != (ExecutionScope{TopologyUID: p.Candidate.TopologyUID, ResourceUID: p.Candidate.ResourceUID, Generation: p.Candidate.Generation}) || g.CommandID != p.Request.RunID {
		return fmt.Errorf("GC disposition candidate/grant mismatch")
	}
	if p.HostBootID != p.Fence.NewBootID {
		return fmt.Errorf("GC disposition boot mismatch")
	}
	if p.Presence != "present" && p.Presence != "absent" {
		return fmt.Errorf("invalid GC disposition presence %q", p.Presence)
	}
	if p.ObservedAt.IsZero() || p.ObservedAt.Before(p.Fence.ObservedAt) {
		return fmt.Errorf("GC observation %s precedes fence receipt %s", p.ObservedAt.Format(time.RFC3339Nano), p.Fence.ObservedAt.Format(time.RFC3339Nano))
	}
	return VerifyHostFenceReceipt(p.Request, p.Fence)
}
func gcDispositionMessage(p GCDispositionProof) ([]byte, error) {
	p.Signature = nil
	raw, err := json.Marshal(p)
	return append([]byte("sysbox.gc-fenced-disposition.v1\x00"), raw...), err
}
func SignGCDisposition(key ed25519.PrivateKey, p GCDispositionProof) (GCDispositionProof, error) {
	if len(key) != ed25519.PrivateKeySize {
		return GCDispositionProof{}, fmt.Errorf("invalid disposition host key")
	}
	if err := p.validate(); err != nil {
		return GCDispositionProof{}, err
	}
	raw, err := gcDispositionMessage(p)
	if err != nil {
		return GCDispositionProof{}, err
	}
	p.Signature = ed25519.Sign(key, raw)
	return p, nil
}
func VerifyGCDisposition(key ed25519.PublicKey, p GCDispositionProof) error {
	if err := p.validate(); err != nil {
		return err
	}
	raw, err := gcDispositionMessage(p)
	if err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, p.Signature) {
		return fmt.Errorf("invalid GC disposition host signature")
	}
	return nil
}
