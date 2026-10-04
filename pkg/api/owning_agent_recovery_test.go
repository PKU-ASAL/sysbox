package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRepublishAssignedRunsRepairsMissingCommand(t *testing.T) {
	s := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()
	run := controlplane.Run{ID: "assigned", Topology: "lab", Op: "apply", Operation: "apply", AgentID: "agent", Status: controlplane.RunAssigned}
	require.NoError(t, s.SaveRun(ctx, run))
	svc := &AgentService{store: s}
	require.NoError(t, svc.RepublishAssignedRuns(ctx))
	cmds, err := s.ListAgentCommands(ctx, "agent")
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "run-"+run.ID, cmds[0].ID)
}
