package controlplane

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
)

// FenceObservationBinding is supplied by canonical recovery authorization, not
// by the worker. The host must match HostBootID before observing resources.
type FenceObservationBinding struct {
	RequestSHA256 string `json:"request_sha256"`
	HostBootID    string `json:"host_boot_id"`
}
type HostObservationProof struct {
	Version         int                     `json:"version"`
	Binding         FenceObservationBinding `json:"binding"`
	DeliveryOwner   string                  `json:"delivery_owner"`
	DeliveryAttempt int                     `json:"delivery_attempt"`
	Signature       []byte                  `json:"signature"`
}

func (b FenceObservationBinding) Validate() error {
	if !validFenceDigest(b.RequestSHA256) || !validFenceBoot(b.HostBootID) {
		return fmt.Errorf("invalid fence observation binding")
	}
	return nil
}
func fenceObservationMessage(report RunRecoveryReport) ([]byte, error) {
	proof := report.HostObservation
	if proof == nil || proof.Version != 1 || proof.Binding.Validate() != nil || proof.DeliveryOwner == "" || proof.DeliveryAttempt < 1 || report.Mode != RunRecoveryObserve || report.CommandID == "" || report.AgentID == "" || report.RunID == "" || report.RunAttempt < 1 || report.QueuedAt.IsZero() || report.Revision == "" || report.TopologyUID == "" || report.StateSerial < 0 || report.ObservedAt.IsZero() || len(report.Resources) == 0 {
		return nil, fmt.Errorf("incomplete host observation proof")
	}
	copy := *proof
	copy.Signature = nil
	report.HostObservation = &copy
	raw, err := json.Marshal(report)
	return append([]byte("sysbox.fence-observation.v1\x00"), raw...), err
}
func SignFenceObservation(key ed25519.PrivateKey, report RunRecoveryReport, binding FenceObservationBinding, owner string, attempt int) (RunRecoveryReport, error) {
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return RunRecoveryReport{}, fmt.Errorf("invalid host observation key")
	}
	if report.HostObservation != nil {
		return RunRecoveryReport{}, fmt.Errorf("observation already carries proof")
	}
	report.HostObservation = &HostObservationProof{Version: 1, Binding: binding, DeliveryOwner: owner, DeliveryAttempt: attempt}
	raw, err := fenceObservationMessage(report)
	if err != nil {
		return RunRecoveryReport{}, err
	}
	report.HostObservation.Signature = ed25519.Sign(key, raw)
	return report, nil
}
func VerifyFenceObservation(key ed25519.PublicKey, report RunRecoveryReport, binding FenceObservationBinding, owner string, attempt int) error {
	if len(key) != ed25519.PublicKeySize || report.HostObservation == nil || report.HostObservation.Binding != binding || report.HostObservation.DeliveryOwner != owner || report.HostObservation.DeliveryAttempt != attempt {
		return fmt.Errorf("host observation binding mismatch")
	}
	raw, err := fenceObservationMessage(report)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, raw, report.HostObservation.Signature) {
		return fmt.Errorf("invalid host observation signature")
	}
	return nil
}
