package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGuestDispatchRecoversAndRetiresWithoutNotification(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "recovery", CommandID: "delivery", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: controlplane.GuestExecutionQueued, Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
		c := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Status: "queued", Protocol: controlplane.AgentProtocolVersion, Execution: &x, ExecutionRequest: x.Request}
		require.NoError(t, s.CreateGuestExecutionDispatch(ctx, x, c))
		// No publish/SaveCommand call: the durable transaction alone must suffice.
		lease, ok, err := s.AcquireAgentCommandLease(ctx, "agent", c.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, s.DeleteAgentCommand(ctx, "agent", lease.ID))
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
	})
}
func TestGuestDispatchSeedSurvivesOperationCAS(t *testing.T) {
	s := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()
	x := controlplane.GuestExecution{ID: "recovery", CommandID: "delivery", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "queued", Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
	c := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Status: "queued", Protocol: controlplane.AgentProtocolVersion, Execution: &x, ExecutionRequest: x.Request}
	require.NoError(t, s.CreateGuestExecutionDispatch(ctx, x, c))
	x.Status = controlplane.GuestExecutionRunning
	ok, err := s.CompareAndSwapGuestExecution(ctx, x, 1)
	require.NoError(t, err)
	require.True(t, ok)
	commands, err := s.ListAgentCommands(ctx, "agent")
	require.NoError(t, err)
	require.Len(t, commands, 1)
}
