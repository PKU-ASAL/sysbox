package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
)

type AdoptionRequest struct {
	Transfer       *OwnershipRetirementProof  `json:"transfer,omitempty"`
	ID             string                     `json:"id"`
	Topology       string                     `json:"topology"`
	TopologyUID    string                     `json:"topology_uid"`
	Address        string                     `json:"address"`
	Provider       string                     `json:"provider"`
	AgentID        string                     `json:"agent_id"`
	ExternalID     string                     `json:"external_id"`
	ResourceUID    string                     `json:"resource_uid"`
	Generation     int64                      `json:"generation"`
	ExpectedSerial int64                      `json:"expected_serial"`
	Evidence       substrate.AdoptionEvidence `json:"evidence"`
}

func (r AdoptionRequest) Validate() error {
	if r.ID == "" || r.Topology == "" || r.TopologyUID == "" || r.Address == "" || r.Provider == "" || r.AgentID == "" || r.ExternalID == "" || r.ResourceUID == "" || r.Generation < 1 || r.ExpectedSerial < 0 {
		return fmt.Errorf("incomplete adoption request")
	}
	if r.Evidence.Provider != r.Provider || r.Evidence.ExternalID != r.ExternalID || r.Evidence.Fingerprint == "" {
		return fmt.Errorf("adoption evidence mismatch")
	}
	if r.Transfer != nil {
		if err := r.Transfer.validate(); err != nil {
			return err
		}
		p := r.Transfer.Plan
		if r.ID != p.ID || r.Topology != p.TargetTopology || r.TopologyUID != p.Target.TopologyUID || r.ResourceUID != p.Target.ResourceUID || r.Generation != p.Target.Generation || r.AgentID != p.AgentID || r.Address != p.TargetAddress || r.Provider != p.Provider || r.ExternalID != p.ExternalID || r.ExpectedSerial != p.TargetSerial || r.Evidence.Fingerprint != p.Fingerprint {
			return fmt.Errorf("transfer adoption binding mismatch")
		}
	}
	return nil
}
func (r AdoptionRequest) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
