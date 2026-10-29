package controlplane

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdoptionInspectionCommandBindsCanonicalTarget(t *testing.T) {
	req := AdoptionInspectionRequest{ID: "inspect", Topology: "lab", TopologyUID: "owner", Address: "sysbox_node.web", Provider: "docker", AgentID: "agent", ExternalID: "external", ResourceUID: "resource", Generation: 1, ExpectedSerial: 3}
	cmd := AgentCommand{ID: "command", Type: "node_operation", AgentID: "agent", TopologyUID: "owner", ActionID: req.ID, ResourceUID: req.ResourceUID, Generation: 1, Operation: NodeOperation{ID: req.ID, Operation: "adoption_inspect", Topology: "lab", Node: "web", Inspection: &req}}
	got, err := FromAdoptionInspectionCommand(cmd)
	require.NoError(t, err)
	require.Equal(t, req, got)
	cmd.ResourceUID = "other"
	_, err = FromAdoptionInspectionCommand(cmd)
	require.Error(t, err)
}
