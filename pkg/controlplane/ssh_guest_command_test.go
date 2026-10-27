package controlplane

import (
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGuestSSHCommandBindsCanonicalMutation(t *testing.T) {
	req := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 4, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	op := NodeOperation{ID: req.ID, ActionID: req.ID, AgentID: "agent", Topology: "lab", Node: "web", ResourceUID: "resource", Generation: 3, Operation: "guest_ssh_access_delete", Status: NodeOperationStatusQueued, SSHMutation: &req}
	cmd := AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", TopologyUID: "owner", ActionID: req.ID, ResourceUID: op.ResourceUID, Generation: op.Generation, Operation: op}
	got, err := FromGuestSSHAccessCommand(cmd)
	require.NoError(t, err)
	require.Equal(t, req, got)
	for _, field := range []string{"type", "agent", "action", "resource", "generation", "node", "topology", "command", "operation", "terminal", "mixed", "missing"} {
		bad := cmd
		switch field {
		case "type":
			bad.Type = "guest_execution"
		case "agent":
			bad.Operation.AgentID = "other"
		case "action":
			bad.ActionID = "other"
		case "resource":
			bad.Operation.ResourceUID = "other"
		case "generation":
			bad.Operation.Generation++
		case "node":
			bad.Operation.Node = ""
		case "topology":
			bad.Operation.Topology = "../other"
		case "command":
			bad.Operation.CommandID = "other"
		case "operation":
			bad.Operation.Operation = "guest_ssh_access_apply"
		case "terminal":
			bad.Operation.Status = NodeOperationStatusDone
		case "mixed":
			bad.Operation.SSHAccess = &SSHAccessRequest{}
		case "missing":
			bad.Operation.SSHMutation = nil
		}
		_, err := FromGuestSSHAccessCommand(bad)
		require.Error(t, err, field)
	}
}
