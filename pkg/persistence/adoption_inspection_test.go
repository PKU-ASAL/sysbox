package persistence

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAdoptionInspectionCompletionPreservesCanonicalRequest(t *testing.T) {
	req := controlplane.AdoptionInspectionRequest{ID: "inspect", Topology: "lab", TopologyUID: "owner", Address: "sysbox_node.web", Provider: "docker", AgentID: "agent", ExternalID: "name", ResourceUID: "resource", Generation: 1}
	digest, err := req.Digest()
	require.NoError(t, err)
	op := controlplane.NodeOperation{ID: req.ID, ActionID: req.ID, CommandID: "command", Topology: req.Topology, AgentID: req.AgentID, Operation: "adoption_inspect", Status: controlplane.NodeOperationStatusQueued, ResourceUID: req.ResourceUID, Generation: 1, Inspection: &req}
	cmd := &CommandRecord{Command: controlplane.AgentCommand{ID: "command", Type: "node_operation", AgentID: "agent", TopologyUID: "owner", ActionID: req.ID, ResourceUID: req.ResourceUID, Generation: 1, Operation: op}}
	receipt := controlplane.SignedExecutionStopReceipt{Grant: controlplane.ExecutionGrant{CommandID: "command", ActionID: req.ID, AgentID: "agent", Operation: "adoption_inspect", Digest: digest, Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}}, StoppedAt: time.Now(), EvidenceDigest: "bound"}
	evidence := substrate.AdoptionEvidence{Provider: "docker", ExternalID: "exact-id", Fingerprint: "sha256:fingerprint", State: json.RawMessage(`{}`)}
	next, err := CompleteAdoptionInspectionFromHost(op, cmd, receipt, controlplane.AdoptionInspectionResult{Evidence: &evidence})
	require.NoError(t, err)
	require.Equal(t, controlplane.NodeOperationStatusDone, next.Status)
	require.Equal(t, &evidence, next.InspectionEvidence)
	require.Error(t, ValidateNodeOperationTransition(op, next), "ordinary writes cannot forge host evidence")
	receipt.Grant.Digest = "other"
	_, err = CompleteAdoptionInspectionFromHost(op, cmd, receipt, controlplane.AdoptionInspectionResult{Evidence: &evidence})
	require.Error(t, err)
}
