package api

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/stretchr/testify/require"
)

func TestRepublishAssignedRunsRepairsMissingCommand(t *testing.T) {
	server := NewServer(t.TempDir(), t.TempDir())
	s := server.apiStore
	ctx := context.Background()
	manager, err := server.stateManager("lab")
	require.NoError(t, err)
	canonical, err := manager.LoadWithContext(ctx)
	require.NoError(t, err)
	run := controlplane.Run{ID: "assigned", Topology: "lab", Op: "apply", Operation: "apply", AgentID: "agent", Status: controlplane.RunAssigned}
	require.NoError(t, s.SaveRun(ctx, run))
	svc := server.agentService()
	require.NoError(t, svc.RepublishAssignedRuns(ctx))
	cmds, err := s.ListAgentCommands(ctx, "agent")
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, controlstore.RunCommandID(run), cmds[0].ID)
	require.Equal(t, canonical.TopologyUID, cmds[0].TopologyUID)
	require.Equal(t, run.ID, cmds[0].ActionID)
	require.Equal(t, canonical.Meta.Serial, cmds[0].ExpectedSerial)
}
