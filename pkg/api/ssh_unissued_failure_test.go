package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSSHDeliveryFailureRequiresProofOfNoExecutionGrant(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		for _, issued := range []bool{false, true} {
			ctx := context.Background()
			id := map[bool]string{false: "unissued", true: "issued"}[issued]
			op := prepareNodeOperation(controlplane.NodeOperation{ID: id, AgentID: "agent", Topology: "lab", Node: "web", Operation: "ssh_access_apply", ResourceUID: id, Generation: 1})
			op.CommandID = "command-" + id
			op.ActionID = id
			require.NoError(t, store.CreateNodeOperation(ctx, op))
			command := controlplane.AgentCommand{ID: op.CommandID, AgentID: "agent", Type: "node_operation", TopologyUID: "owner", ResourceUID: id, Generation: 1, ActionID: id, Operation: op}
			require.NoError(t, store.SaveAgentCommand(ctx, command))
			leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", command.ID, "worker", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			if issued {
				authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
				session, err := authority.OpenSession(ctx, "agent", "session", "")
				require.NoError(t, err)
				_, err = authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: id, Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: command.ID, ActionID: id, Attempt: 1, Operation: "ssh_access_apply", Digest: "digest", TargetBootID: "host"}, time.Minute)
				require.NoError(t, err)
			}
			failed, err := store.ApplyAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: command.ID, AgentID: "agent", Type: "node_operation", Status: "failed", LeaseOwner: leased.LeaseOwner, Attempt: leased.Attempt, ActionID: id, ResourceUID: id, Generation: 1})
			require.NoError(t, err)
			require.NoError(t, (&AgentService{store: store}).reconcileUnissuedNodeFailures(ctx))
			require.NoError(t, reconcileUnissuedNodeFailure(ctx, store, *failed))
			current, err := store.GetNodeOperation(ctx, id)
			require.NoError(t, err)
			if issued {
				require.Equal(t, controlplane.NodeOperationStatusQueued, current.Status)
			} else {
				require.Equal(t, controlplane.NodeOperationStatusFailed, current.Status)
				require.Contains(t, current.Err, "before execution authorization")
			}
		}
	})
}
