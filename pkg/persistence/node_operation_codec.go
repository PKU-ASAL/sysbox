package persistence

import (
	"bytes"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func EncodeNodeOperation(x controlplane.NodeOperation) ([]byte, error) {
	if !topologyName.MatchString(x.ID) || x.AgentID == "" || x.Topology == "" || !validNodeOperationStatus(x.Status) || !validSSHResultState(x) || !validInspectionState(x) {
		return nil, ErrInvalidRecord
	}
	return json.Marshal(struct {
		Version   int                        `json:"version"`
		Operation controlplane.NodeOperation `json:"operation"`
		CommandID string                     `json:"command_id"`
	}{1, x, x.CommandID})
}
func DecodeNodeOperation(raw []byte) (*controlplane.NodeOperation, error) {
	var record struct {
		Version   int                        `json:"version"`
		Operation controlplane.NodeOperation `json:"operation"`
		CommandID string                     `json:"command_id"`
	}
	if err := decodeGuestObject(raw, &record); err != nil {
		return nil, err
	}
	x := record.Operation
	x.CommandID = record.CommandID
	if record.Version != 1 {
		return nil, ErrUnsupportedVersion
	}
	if !topologyName.MatchString(x.ID) || x.AgentID == "" || x.Topology == "" || !validNodeOperationStatus(x.Status) || !validSSHResultState(x) || !validInspectionState(x) {
		return nil, ErrCorruptRecord
	}
	return &x, nil
}

func validSSHResultState(x controlplane.NodeOperation) bool {
	if x.SSHResult != nil {
		return controlplane.IsGuestSSHAccessOperation(x.Operation) && x.SSHMutation != nil && x.Status == controlplane.NodeOperationStatusDone
	}
	return !controlplane.IsGuestSSHAccessOperation(x.Operation) || x.Status != controlplane.NodeOperationStatusDone
}

func validNodeOperationStatus(status string) bool {
	switch status {
	case controlplane.NodeOperationStatusQueued, controlplane.NodeOperationStatusRunning, controlplane.NodeOperationStatusDone, controlplane.NodeOperationStatusFailed:
		return true
	}
	return false
}
func SameNodeOperation(a, b controlplane.NodeOperation) bool {
	aa, err := EncodeNodeOperation(a)
	if err != nil {
		return false
	}
	bb, err := EncodeNodeOperation(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aa, bb)
}

func validInspectionState(x controlplane.NodeOperation) bool {
	if x.Inspection != nil && (x.Operation != "adoption_inspect" || x.Inspection.Validate() != nil) {
		return false
	}
	if x.InspectionEvidence != nil {
		return x.Operation == "adoption_inspect" && x.Inspection != nil && x.Status == controlplane.NodeOperationStatusDone && x.InspectionEvidence.Provider == x.Inspection.Provider && x.InspectionEvidence.ExternalID != "" && x.InspectionEvidence.Fingerprint != "" && json.Valid(x.InspectionEvidence.State)
	}
	return x.Operation != "adoption_inspect" || x.Status != controlplane.NodeOperationStatusDone
}
