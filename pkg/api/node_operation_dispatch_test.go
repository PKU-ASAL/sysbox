package api

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func nodeDispatchFixture() (controlplane.NodeOperation, controlplane.AgentCommand) {
	op := controlplane.NodeOperation{ID: "op", CommandID: "node-op-op", AgentID: "agent", Topology: "lab", Workspace: "lab", Operation: "pause", Node: "web", Status: controlplane.NodeOperationStatusQueued, ActionID: "op", ResourceUID: "resource", Generation: 2, CreatedAt: time.Now().UTC()}
	op.LeaseEpoch = 1
	cmd := controlplane.AgentCommand{ID: op.CommandID, AgentID: op.AgentID, Type: "node_operation", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, CreatedAt: op.CreatedAt, Operation: op, ActionID: op.ActionID, ResourceUID: op.ResourceUID, Generation: op.Generation}
	cmd.LeaseEpoch = op.LeaseEpoch
	return op, cmd
}

func TestNodeOperationDispatchIsDurableBeforeDelivery(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		op, cmd := nodeDispatchFixture()
		require.NoError(t, s.CreateNodeOperationDispatch(ctx, op, cmd))
		stored, err := s.GetNodeOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, op, *stored)
		commands, err := s.ListAgentCommands(ctx, cmd.AgentID)
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, stored.CommandID, commands[0].ID)
		next := *stored
		next.Status = controlplane.NodeOperationStatusRunning
		require.NoError(t, s.CompareAndSwapNodeOperation(ctx, *stored, next))
		commands, err = s.ListAgentCommands(ctx, cmd.AgentID)
		require.NoError(t, err)
		require.Len(t, commands, 1, "operation updates must preserve outbox seed")
		lease, ok, err := s.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, s.DeleteAgentCommand(ctx, cmd.AgentID, lease.ID))
		commands, err = s.ListAgentCommands(ctx, cmd.AgentID)
		require.NoError(t, err)
		require.Empty(t, commands, "retirement must suppress the immutable seed")
	})
}

type failNodeDispatch struct {
	apiStore
	failure error
}

func (s failNodeDispatch) CreateNodeOperationDispatch(context.Context, controlplane.NodeOperation, controlplane.AgentCommand) error {
	return s.failure
}

func TestNodeOperationWakeupFollowsAtomicCommit(t *testing.T) {
	root := t.TempDir()
	store := &localAPIStore{runsDir: root}
	op, _ := nodeDispatchFixture()
	wakes := 0
	service := &NodeOperationService{operations: newNodeOperationStore(store), wakeCommand: func(agent string, cmd controlplane.AgentCommand) error {
		wakes++
		reopened := &localAPIStore{runsDir: root}
		saved, err := reopened.GetNodeOperation(context.Background(), op.ID)
		require.NoError(t, err)
		require.Equal(t, cmd.ID, saved.CommandID)
		commands, err := reopened.ListAgentCommands(context.Background(), agent)
		require.NoError(t, err)
		require.Len(t, commands, 1)
		return errors.New("offline wakeup")
	}}
	_, err := service.enqueue(context.Background(), op)
	require.NoError(t, err)
	require.Equal(t, 1, wakes)
	failure := errors.New("commit rejected")
	service.operations = newNodeOperationStore(failNodeDispatch{apiStore: store, failure: failure})
	op.ID = "other"
	_, err = service.enqueue(context.Background(), op)
	require.ErrorIs(t, err, failure)
	require.Equal(t, 1, wakes)
}

func TestNodeOperationDispatchConcurrentCreationHasOneWinner(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		op, cmd := nodeDispatchFixture()
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); results <- s.CreateNodeOperationDispatch(context.Background(), op, cmd) }()
		}
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			}
		}
		require.Equal(t, 1, wins)
		commands, err := s.ListAgentCommands(context.Background(), cmd.AgentID)
		require.NoError(t, err)
		require.Len(t, commands, 1)
	})
}

func TestNodeOperationDispatchConflictRollsBackOperation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		op, cmd := nodeDispatchFixture()
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		require.Error(t, s.CreateNodeOperationDispatch(ctx, op, cmd))
		_, err := s.GetNodeOperation(ctx, op.ID)
		require.Error(t, err, "failed command insert must not leave unbound operation")
	})
}
