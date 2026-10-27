package persistence

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"strings"
	"time"
)

func EncodeInventory(x controlplane.AgentInventory) ([]byte, error) {
	if strings.TrimSpace(x.AgentID) == "" {
		return nil, ErrInvalidRecord
	}
	if x.ObservedAt.IsZero() {
		x.ObservedAt = time.Now().UTC()
	}
	x.ObservedAt = x.ObservedAt.UTC()
	return json.Marshal(x)
}
func DecodeInventory(raw []byte) (*controlplane.AgentInventory, error) {
	var x controlplane.AgentInventory
	if err := decodeGuestObject(raw, &x); err != nil {
		return nil, err
	}
	if strings.TrimSpace(x.AgentID) == "" {
		return nil, ErrCorruptRecord
	}
	return &x, nil
}
func EncodeProjection(x controlplane.ResourceProjection) ([]byte, error) {
	if strings.TrimSpace(x.Topology) == "" {
		return nil, ErrInvalidRecord
	}
	x.ObservedAt = x.ObservedAt.UTC()
	return json.Marshal(x)
}
func DecodeProjection(raw []byte) (*controlplane.ResourceProjection, error) {
	var x controlplane.ResourceProjection
	if err := decodeGuestObject(raw, &x); err != nil {
		return nil, err
	}
	if strings.TrimSpace(x.Topology) == "" {
		return nil, ErrCorruptRecord
	}
	return &x, nil
}
