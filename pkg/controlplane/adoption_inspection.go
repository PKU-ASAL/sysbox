package controlplane

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Inspection is read-only. Its result supplies evidence for a separate,
// explicitly confirmed adoption; it never grants ownership itself.
type AdoptionInspectionRequest struct {
	ID             string `json:"id"`
	Topology       string `json:"topology"`
	TopologyUID    string `json:"topology_uid"`
	Address        string `json:"address"`
	Provider       string `json:"provider"`
	AgentID        string `json:"agent_id"`
	ExternalID     string `json:"external_id"`
	ResourceUID    string `json:"resource_uid"`
	Generation     int64  `json:"generation"`
	ExpectedSerial int64  `json:"expected_serial"`
}

func (r AdoptionInspectionRequest) Validate() error {
	a, err := address.Parse(r.Address)
	if err != nil || a.Type != "sysbox_node" || r.ID == "" || r.Topology == "" || r.TopologyUID == "" || r.Provider == "" || r.AgentID == "" || r.ExternalID == "" || r.ResourceUID == "" || r.Generation < 1 || r.ExpectedSerial < 0 {
		return fmt.Errorf("incomplete adoption inspection identity")
	}
	return nil
}
func (r AdoptionInspectionRequest) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}
func FromAdoptionInspectionCommand(cmd AgentCommand) (AdoptionInspectionRequest, error) {
	if cmd.Type != "node_operation" || cmd.Operation.Operation != "adoption_inspect" || cmd.Operation.Inspection == nil || cmd.ID == "" {
		return AdoptionInspectionRequest{}, fmt.Errorf("adoption inspection command required")
	}
	req := *cmd.Operation.Inspection
	if req.ID != cmd.ActionID || req.ID != cmd.Operation.ID || req.Topology != cmd.Operation.Topology || req.AgentID != cmd.AgentID || req.TopologyUID != cmd.TopologyUID || req.ResourceUID != cmd.ResourceUID || req.Generation != cmd.Generation {
		return req, fmt.Errorf("adoption inspection target mismatch")
	}
	return req, req.Validate()
}

// An inspection error is a known read-only failure, not an unknown mutation.
type AdoptionInspectionResult struct {
	Evidence *substrate.AdoptionEvidence `json:"evidence,omitempty"`
	Error    string                      `json:"error,omitempty"`
}
