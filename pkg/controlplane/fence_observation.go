package controlplane

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	if proof == nil || (proof.Version != 1 && proof.Version != 2) || proof.Binding.Validate() != nil || proof.DeliveryOwner == "" || proof.DeliveryAttempt < 1 || report.Mode != RunRecoveryObserve || report.CommandID == "" || report.AgentID == "" || report.RunID == "" || report.RunAttempt < 1 || report.QueuedAt.IsZero() || report.Revision == "" || report.TopologyUID == "" || report.StateSerial < 0 || report.ObservedAt.IsZero() || len(report.Resources) == 0 {
		return nil, fmt.Errorf("incomplete host observation proof")
	}
	copy := *proof
	copy.Signature = nil
	report.HostObservation = &copy
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	if proof.Version == 2 {
		return append([]byte("sysbox.fence-observation.v2\x00"), raw...), nil
	}
	return append([]byte("sysbox.fence-observation.v1\x00"), raw...), nil
}
func SignFenceObservation(key ed25519.PrivateKey, report RunRecoveryReport, binding FenceObservationBinding, owner string, attempt int) (RunRecoveryReport, error) {
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return RunRecoveryReport{}, fmt.Errorf("invalid host observation key")
	}
	if report.HostObservation != nil {
		return RunRecoveryReport{}, fmt.Errorf("observation already carries proof")
	}
	report.HostObservation = &HostObservationProof{Version: 2, Binding: binding, DeliveryOwner: owner, DeliveryAttempt: attempt}
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

// Version 2 reports have one JSON representation across JSONB storage, signed
// evidence and disposition digests, including embedded provider RawMessages.
func (r RunRecoveryReport) MarshalJSON() ([]byte, error) {
	type wire RunRecoveryReport
	raw, err := json.Marshal(wire(r))
	if err != nil || r.HostObservation == nil || r.HostObservation.Version != 2 {
		return raw, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return nil, err
	}
	value, err = normalizeEvidenceNumbers(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func normalizeEvidenceNumbers(value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			next, err := normalizeEvidenceNumbers(item)
			if err != nil {
				return nil, err
			}
			v[k] = next
		}
	case []any:
		for i, item := range v {
			next, err := normalizeEvidenceNumbers(item)
			if err != nil {
				return nil, err
			}
			v[i] = next
		}
	case json.Number:
		raw := string(v)
		negative := strings.HasPrefix(raw, "-")
		raw = strings.TrimPrefix(raw, "-")
		exponent := 0
		if i := strings.IndexAny(raw, "eE"); i >= 0 {
			n, err := strconv.Atoi(raw[i+1:])
			if err != nil || n > 131072 || n < -16383 {
				return nil, fmt.Errorf("evidence number exponent outside supported bounds")
			}
			exponent = n
			raw = raw[:i]
		}
		if i := strings.IndexByte(raw, '.'); i >= 0 {
			exponent -= len(raw) - i - 1
			raw = raw[:i] + raw[i+1:]
		}
		raw = strings.TrimLeft(raw, "0")
		if raw == "" {
			return json.Number("0"), nil
		}
		trimmed := strings.TrimRight(raw, "0")
		exponent += len(raw) - len(trimmed)
		raw = trimmed
		if len(raw)+exponent > 131072 || exponent < -16383 {
			return nil, fmt.Errorf("evidence number outside supported bounds")
		}
		point := len(raw) + exponent
		if exponent >= 0 {
			raw += strings.Repeat("0", exponent)
		} else if point > 0 {
			raw = raw[:point] + "." + raw[point:]
		} else {
			raw = "0." + strings.Repeat("0", -point) + raw
		}
		if negative {
			raw = "-" + raw
		}
		return json.Number(raw), nil
	}
	return value, nil
}
