package agentexec

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCancellationReportsTargetProof(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		r := newCommandRunner(nil, Options{})
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
		r.Execute(context.Background(), &controlplane.AgentCommand{ID: "cancel", Type: "cancel_command", Operation: controlplane.NodeOperation{ExternalID: target.ID}}, func(e controlplane.AgentCommandEvent) { events = append(events, e) })
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.Len(t, events, 2)
		e := events[0]
		require.Equal(t, target.ID, e.CommandID)
		require.Equal(t, target.Type, e.Type)
		require.Equal(t, target.ActionID, e.ActionID)
		require.Equal(t, target.ResourceUID, e.ResourceUID)
		require.Equal(t, target.Generation, e.Generation)
		require.Equal(t, target.LeaseEpoch, e.LeaseEpoch)
		require.Equal(t, target.LeaseOwner, e.LeaseOwner)
		require.Equal(t, target.Attempt, e.Attempt)
	}
}
