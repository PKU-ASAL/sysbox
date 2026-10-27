package persistence

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"strings"
	"time"
)

func NormalizeCommandEvent(e controlplane.AgentCommandEvent) controlplane.AgentCommandEvent {
	if e.AgentID == "" {
		e.AgentID = "_unknown"
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	e.CreatedAt = e.CreatedAt.UTC()
	return e
}
func ValidateCommandEvent(e controlplane.AgentCommandEvent) error {
	e = NormalizeCommandEvent(e)
	if strings.TrimSpace(e.CommandID) == "" || e.AgentID == "" || e.Type == "" || e.Status == "" {
		return ErrInvalidRecord
	}
	return nil
}
func EncodeCommandEvent(e controlplane.AgentCommandEvent) ([]byte, error) {
	e = NormalizeCommandEvent(e)
	if err := ValidateCommandEvent(e); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}
func DecodeCommandEvent(raw []byte) (*controlplane.AgentCommandEvent, error) {
	var e controlplane.AgentCommandEvent
	if err := decodeGuestObject(raw, &e); err != nil {
		return nil, err
	}
	if ValidateCommandEvent(e) != nil {
		return nil, ErrCorruptRecord
	}
	e = NormalizeCommandEvent(e)
	return &e, nil
}
