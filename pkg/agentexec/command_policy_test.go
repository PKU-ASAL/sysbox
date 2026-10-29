package agentexec

import (
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestAuthorizeAgentCommandPolicy(t *testing.T) {
	denyConsole := false
	denyImport := false
	policy := config.AgentPolicyConfig{
		AllowedCommands:   []string{"run_assigned", "session_open", "node_operation"},
		AllowedWorkspaces: []string{"lab"},
		AllowedSubstrates: []string{"docker"},
		AllowConsole:      &denyConsole,
		AllowImport:       &denyImport,
	}

	require.NoError(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type: "run_assigned",
		Run:  &controlplane.Run{Workspace: "lab", Topology: "lab"},
	}))
	require.ErrorContains(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type: "run_assigned",
		Run:  &controlplane.Run{Workspace: "prod", Topology: "prod"},
	}), "workspace")
	require.ErrorContains(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type:    "session_open",
		Session: &controlplane.ConsoleSession{Workspace: "lab", Topology: "lab"},
	}), "console")
	require.ErrorContains(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type: "node_operation",
		Operation: controlplane.NodeOperation{
			Workspace: "lab",
			Topology:  "lab",
			Substrate: "firecracker",
			Operation: "pause",
		},
	}), "substrate")
	require.ErrorContains(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type: "node_operation",
		Operation: controlplane.NodeOperation{
			Workspace: "lab",
			Topology:  "lab",
			Substrate: "docker",
			Operation: "import",
		},
	}), "import")
}
