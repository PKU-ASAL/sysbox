package state

import (
	"encoding/json"
	"fmt"
)

type PrivateEnvelope struct {
	Version int             `json:"version"`
	Payload json.RawMessage `json:"payload"`
}

type DriverPrivate struct {
	ProviderState json.RawMessage `json:"provider_state,omitempty"`
	Runtime       map[string]any  `json:"runtime,omitempty"`
}

func (r *Resource) SetProviderState(raw []byte) error {
	var current DriverPrivate
	if len(r.Private) > 0 {
		if err := DecodePrivate(r.Private, 1, &current); err != nil {
			return err
		}
	}
	current.ProviderState = append(json.RawMessage(nil), raw...)
	encoded, err := EncodePrivate(1, current)
	if err != nil {
		return err
	}
	r.Private = encoded
	return nil
}
func (r *Resource) ProviderState() ([]byte, error) {
	if len(r.Private) == 0 {
		return nil, nil
	}
	var current DriverPrivate
	if err := DecodePrivate(r.Private, 1, &current); err != nil {
		return nil, err
	}
	return append([]byte(nil), current.ProviderState...), nil
}

func EncodePrivate(version int, payload any) (json.RawMessage, error) {
	if version <= 0 {
		return nil, fmt.Errorf("private state version must be positive")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(PrivateEnvelope{Version: version, Payload: raw})
}

func DecodePrivate(raw json.RawMessage, expected int, target any) error {
	if len(raw) == 0 {
		return nil
	}
	var envelope PrivateEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode private state envelope: %w", err)
	}
	if envelope.Version != expected {
		return fmt.Errorf("private state version %d is incompatible; expected %d", envelope.Version, expected)
	}
	if err := json.Unmarshal(envelope.Payload, target); err != nil {
		return fmt.Errorf("decode private state payload: %w", err)
	}
	return nil
}
