package agentexec

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestCancellationRequestDoesNotReportTargetStopped(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		r := newCommandRunner(Options{})
		target := controlplane.AgentCommand{ID: "target", Type: "node_operation", LeaseOwner: "owner", Attempt: 2}
		if fenced {
			target.ActionID = "action"
			target.ResourceUID = "uid"
			target.Generation = 2
			target.LeaseEpoch = 4
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r.track(&target, cancel)
		var events []controlplane.AgentCommandEvent
		request := controlplane.AgentCommand{ID: "cancel", Type: "cancel_command", LeaseOwner: "cancel-owner", Attempt: 3, Operation: controlplane.NodeOperation{ExternalID: target.ID}}
		r.Execute(context.Background(), &request, func(e controlplane.AgentCommandEvent) { events = append(events, e) })
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.Len(t, events, 1, "context cancellation is not target completion evidence")
		e := events[0]
		require.Equal(t, "cancel", e.CommandID)
		require.Equal(t, "completed", e.Status)
		require.Equal(t, "cancellation requested", e.Message)
		require.Equal(t, request.LeaseOwner, e.LeaseOwner)
		require.Equal(t, request.Attempt, e.Attempt)
	}
}

func TestOldRunnerCleanupKeepsReplacementCancellationTarget(t *testing.T) {
	r := newCommandRunner(Options{})
	cmd := controlplane.AgentCommand{ID: "command", Type: "guest_execution", Execution: &controlplane.GuestExecution{ID: "operation"}}
	oldCtx, oldCancel := context.WithCancel(context.Background())
	defer oldCancel()
	newCtx, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	old := r.track(&cmd, oldCancel)
	r.track(&cmd, newCancel)
	r.untrack(old)
	r.Execute(context.Background(), &controlplane.AgentCommand{ID: "cancel", Type: "cancel_command", Operation: controlplane.NodeOperation{ExternalID: "operation"}}, nil)
	require.ErrorIs(t, newCtx.Err(), context.Canceled)
	require.NoError(t, oldCtx.Err())
}
