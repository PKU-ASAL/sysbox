package controlplane

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestNodePowerDigestBindsOnlyImmutableOperation(t *testing.T) {
	cmd := AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", TopologyUID: "owner", ResourceUID: "resource", Generation: 2, ActionID: "action", Operation: NodeOperation{Topology: "lab", Node: "vm", Operation: "pause"}}
	req, err := FromNodeOperationCommand(cmd)
	require.NoError(t, err)
	before, err := req.Digest()
	require.NoError(t, err)
	cmd.Operation.Status = "running"
	cmd.Operation.StartedAt = time.Now()
	cmd.LeaseOwner = "worker"
	cmd.Attempt = 3
	req, err = FromNodeOperationCommand(cmd)
	require.NoError(t, err)
	after, err := req.Digest()
	require.NoError(t, err)
	require.Equal(t, before, after)
	req.Operation = "resume"
	changed, err := req.Digest()
	require.NoError(t, err)
	require.NotEqual(t, before, changed)
	for _, change := range []func(*AgentCommand){func(c *AgentCommand) { c.Type = "other" }, func(c *AgentCommand) { c.TopologyUID = "" }, func(c *AgentCommand) { c.ActionID = "" }, func(c *AgentCommand) { c.ResourceUID = "" }, func(c *AgentCommand) { c.Generation = 0 }, func(c *AgentCommand) { c.Operation.Operation = "import" }, func(c *AgentCommand) { c.Operation.ExternalID = "arbitrary-domain" }, func(c *AgentCommand) { c.Operation.Node = "" }, func(c *AgentCommand) { c.Operation.Topology = "../outside" }} {
		bad := cmd
		change(&bad)
		_, err := FromNodeOperationCommand(bad)
		require.Error(t, err)
	}
}
