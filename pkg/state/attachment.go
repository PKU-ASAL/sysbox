package state

import (
	"encoding/json"

	"github.com/oslab/sysbox/pkg/address"
)

// Attachment is the provider-independent network identity and durable state
// for one logical node-to-network connection.
type Attachment struct {
	Name        string                `json:"name"`
	Node        address.Address       `json:"node"`
	Network     address.Address       `json:"network"`
	MAC         string                `json:"mac"`
	IPPrefixes  []string              `json:"ip_prefixes,omitempty"`
	Gateway     string                `json:"gateway,omitempty"`
	Driver      string                `json:"driver"`
	Observation AttachmentObservation `json:"observation,omitempty"`
	DriverState json.RawMessage       `json:"driver_state,omitempty"`
}

// AttachmentObservation records non-semantic device details reported by a
// driver. Changes here do not change attachment identity.
type AttachmentObservation struct {
	GuestDevice string `json:"guest_device,omitempty"`
}
