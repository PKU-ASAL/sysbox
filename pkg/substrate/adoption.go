package substrate

import "encoding/json"

// AdoptionEvidence is an inspection receipt, not ownership. The provider must
// compare it again before writing an explicit ownership binding.
type AdoptionEvidence struct {
	ManagedIdentity *OperationIdentity `json:"managed_identity,omitempty"`
	Provider        string             `json:"provider"`
	ExternalID      string             `json:"external_id"`
	Fingerprint     string             `json:"fingerprint"`
	State           json.RawMessage    `json:"state"`
}
