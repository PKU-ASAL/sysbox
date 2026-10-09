package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGuestExecutionCancellationNotificationFailureKeepsCommittedOutbox(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	svc := newGuestExecutionService(store, nil, nil)
	x, err := svc.Create(context.Background(), "lab", "node", "agent", controlplane.GuestExecutionRequest{Argv: []string{"sleep", "10"}})
	require.NoError(t, err)
	failure := errors.New("outbox unavailable")
	svc.publish = func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		return controlplane.AgentCommand{}, failure
	}
	_, err = svc.Cancel(context.Background(), x.ID)
	require.NoError(t, err, "notification is only a hint after atomic acceptance")
	commands, err := store.ListAgentCommands(context.Background(), "agent")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	stored, err := store.GetGuestExecution(context.Background(), x.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionQueued, stored.Status, "request is not stop evidence")
	require.True(t, stored.CancelRequested)
	require.True(t, stored.EndedAt.IsZero())
	require.True(t, stored.ExpiresAt.IsZero())
	_, err = svc.MarkRunning(context.Background(), x.ID)
	require.Error(t, err, "cancellation intent must prevent a subsequent start")
	var ids []string
	svc.publish = func(_ context.Context, _ string, c controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		ids = append(ids, c.ID)
		return c, nil
	}
	_, err = svc.Cancel(context.Background(), x.ID)
	require.NoError(t, err)
	_, err = svc.Cancel(context.Background(), x.ID)
	require.NoError(t, err)
	require.Len(t, ids, 2)
	require.NotEmpty(t, ids[0])
	require.Equal(t, ids[0], ids[1])
}

func TestGuestExecutionCancellationRecoversOutboxAndPreservesLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		svc := newGuestExecutionService(store, nil, nil)
		x, err := svc.Create(ctx, "lab", "node", "agent", controlplane.GuestExecutionRequest{Argv: []string{"sleep", "10"}})
		require.NoError(t, err)
		x, err = svc.Cancel(ctx, x.ID) // intent committed, no publisher attached
		require.NoError(t, err)
		require.True(t, x.CancelRequested)
		cold := &AgentService{store: store}
		require.NoError(t, cold.reconcileGuestExecutionCancellations(ctx))
		require.NoError(t, cold.reconcileGuestExecutionCancellations(ctx))
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", commands[0].ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		svc = newGuestExecutionService(store, cold.PublishCommand, nil)
		again, err := svc.Cancel(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, x.Version, again.Version, "intent retry does not advance version")
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, *leased, commands[0])
		cleared := again
		cleared.CancelRequested = false
		ok, err = store.CompareAndSwapGuestExecution(ctx, cleared, again.Version)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.False(t, ok)
		started := again
		started.Status = controlplane.GuestExecutionRunning
		ok, err = store.CompareAndSwapGuestExecution(ctx, started, again.Version)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.False(t, ok)
		rebound := again
		rebound.CommandID = "another-command"
		ok, err = store.CompareAndSwapGuestExecution(ctx, rebound, again.Version)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.False(t, ok)
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", leased.ID))
		require.NoError(t, cold.reconcileGuestExecutionCancellations(ctx))
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
		activities, err := store.ListGuestActivity(ctx, "lab")
		require.NoError(t, err)
		require.Len(t, activities, 1)
		require.True(t, activities[0].Unresolved(), "retired cancellation is not stop proof")
	})
}

func TestGuestExecutionCancellationDoesNotReplaceRealCompletion(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "operation", CommandID: "source", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", Status: "running", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveGuestExecution(ctx, x))
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, Status: "leased", LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute), Attempt: 1, ActionID: "action", ResourceUID: "resource", Generation: 1, LeaseEpoch: 1}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		svc := newGuestExecutionService(store, (&AgentService{store: store}).PublishCommand, nil)
		pending, err := svc.Cancel(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, "running", pending.Status)
		require.True(t, pending.CancelRequested)
		completed, err := store.CompleteGuestExecution(ctx, x.AgentID, x.ID, controlplane.GuestExecutionCompletion{ResultClass: "exit", Result: controlplane.GuestExecutionResult{Stdout: "finished"}, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, LeaseEpoch: cmd.LeaseEpoch, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt})
		require.NoError(t, err)
		require.Equal(t, "completed", completed.Status, "completion may win the race with cancellation")
		require.True(t, completed.CancelRequested)
		require.Equal(t, "finished", completed.Result.Stdout)
	})
}

func TestGuestCancellationSQLSchemaRequiresRebuild(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		switch s := store.(type) {
		case *sqliteAPIStore:
			db, err := s.open()
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, "UPDATE sysbox_api_schema_migrations SET version=11 WHERE name='api'")
			require.NoError(t, err)
			_, err = (&sqliteAPIStore{dbPath: s.dbPath}).open()
			require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
		case *postgresAPIStore:
			pool, err := s.poolFor(ctx)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, "UPDATE sysbox_api_schema_migrations SET version=11 WHERE name='api'")
			require.NoError(t, err)
			_, err = (&postgresAPIStore{dsn: s.dsn}).poolFor(ctx)
			require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
		}
	})
}
