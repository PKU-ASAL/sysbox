package agentexec

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSSHAccessCommandRequiresBoundPayload(t *testing.T) {
	cmd := controlplane.AgentCommand{Type: "node_operation", ID: "command", AgentID: "agent", TopologyUID: "topology", ActionID: "action", ResourceUID: "resource", Generation: 1, Operation: controlplane.NodeOperation{Operation: "ssh_access_apply", SSHAccess: &controlplane.SSHAccessRequest{ID: "different", Topology: "lab", Node: "web", AuthorizedKeys: []string{"ssh-ed25519 AAAA key"}}}}
	_, err := controlplane.FromSSHAccessCommand(cmd)
	require.Error(t, err)
}
