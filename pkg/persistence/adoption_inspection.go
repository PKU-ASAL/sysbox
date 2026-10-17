package persistence

import (
	"bytes"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// Called by authenticated host completion under command/operation exclusion.
func CompleteAdoptionInspectionFromHost(current controlplane.NodeOperation, record *CommandRecord, receipt controlplane.SignedExecutionStopReceipt, result controlplane.AdoptionInspectionResult) (controlplane.NodeOperation, error) {
	if record == nil || current.Inspection == nil || current.Operation != "adoption_inspect" || current.ID != receipt.Grant.ActionID || current.CommandID != record.Command.ID || current.AgentID != receipt.Grant.AgentID || receipt.StoppedAt.IsZero() || receipt.EvidenceDigest == "" {
		return current, ErrConflict
	}
	if (result.Evidence == nil) == (result.Error == "") {
		return current, ErrConflict
	}
	if e := result.Evidence; e != nil && (e.Provider != current.Inspection.Provider || e.ExternalID == "" || e.Fingerprint == "" || !json.Valid(e.State)) {
		return current, ErrConflict
	}
	cmd := record.Command
	cmd.Operation = current
	req, err := controlplane.FromAdoptionInspectionCommand(cmd)
	if err != nil {
		return current, err
	}
	digest, err := req.Digest()
	g := receipt.Grant
	if err != nil || g.Digest != digest || g.Operation != "adoption_inspect" || g.CommandID != cmd.ID || g.AgentID != cmd.AgentID || g.Scope != (controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}) {
		return current, ErrConflict
	}
	if current.Status == controlplane.NodeOperationStatusDone || current.Status == controlplane.NodeOperationStatusFailed {
		prior := controlplane.AdoptionInspectionResult{Evidence: current.InspectionEvidence, Error: current.Err}
		a, err := canonicalInspectionResult(prior)
		if err != nil {
			return current, err
		}
		b, err := canonicalInspectionResult(result)
		if err != nil {
			return current, err
		}
		if !bytes.Equal(a, b) {
			return current, ErrConflict
		}
		return current, nil
	}
	next := current
	next.Status = controlplane.NodeOperationStatusDone
	next.EndedAt = receipt.StoppedAt
	next.Err = result.Error
	next.InspectionEvidence = result.Evidence
	if result.Error != "" {
		next.Status = controlplane.NodeOperationStatusFailed
	}
	if err := validateNodeOperationTransition(current, next, true); err != nil {
		return current, err
	}
	return next, nil
}

func canonicalInspectionResult(result controlplane.AdoptionInspectionResult) ([]byte, error) {
	if result.Evidence != nil {
		copy := *result.Evidence
		var value any
		decoder := json.NewDecoder(bytes.NewReader(copy.State))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		copy.State = raw
		result.Evidence = &copy
	}
	return json.Marshal(result)
}
