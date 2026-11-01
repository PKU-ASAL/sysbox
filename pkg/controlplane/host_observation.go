package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"
)

type HostObservation struct {
	AgentID     string               `json:"agent_id"`
	SessionID   string               `json:"session_id"`
	Sequence    uint64               `json:"sequence"`
	BootID      string               `json:"boot_id"`
	ObservedAt  time.Time            `json:"observed_at"`
	Inventory   AgentInventory       `json:"inventory"`
	Projections []ResourceProjection `json:"projections"`
	Signature   []byte               `json:"signature"`
}

func (o HostObservation) signingBytes() ([]byte, error) {
	if o.AgentID == "" || o.SessionID == "" || o.Sequence == 0 || o.BootID == "" || o.ObservedAt.IsZero() || o.Inventory.AgentID != o.AgentID {
		return nil, fmt.Errorf("invalid host observation identity")
	}
	o.Signature = nil
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	if len(raw) > 8<<20 {
		return nil, fmt.Errorf("host observation exceeds limit")
	}
	return append([]byte("sysbox-host-observation-v1\x00"), raw...), nil
}

func SignHostObservation(key ed25519.PrivateKey, o HostObservation) (HostObservation, error) {
	if len(key) != ed25519.PrivateKeySize {
		return HostObservation{}, fmt.Errorf("host observation signing key required")
	}
	raw, err := o.signingBytes()
	if err != nil {
		return HostObservation{}, err
	}
	o.Signature = ed25519.Sign(key, raw)
	return o, nil
}

func VerifyHostObservation(key ed25519.PublicKey, o HostObservation) error {
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("host observation public key required")
	}
	raw, err := o.signingBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, raw, o.Signature) {
		return fmt.Errorf("host observation signature rejected")
	}
	return nil
}

// Observations expire even if the worker keeps sending heartbeats. A delayed
// signed snapshot is evidence of a past read, never perpetual current health.
const HostObservationMaxAge = time.Minute

func FreshHostObservation(observed, now time.Time) bool {
	return !observed.IsZero() && !observed.After(now.Add(time.Minute)) && now.Sub(observed) <= HostObservationMaxAge
}
