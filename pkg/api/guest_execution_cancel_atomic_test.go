package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGuestCancellationCommitsIntentAndOutbox(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		svc := newGuestExecutionService(store, nil, nil)
		x, err := svc.Create(ctx, "lab", "node", "agent", controlplane.GuestExecutionRequest{Argv: []string{"sleep", "30"}})
		require.NoError(t, err)
		pending, err := svc.Cancel(ctx, x.ID)
		require.NoError(t, err)
		require.True(t, pending.CancelRequested)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1, "intent must not commit without its durable cancellation delivery")
		require.Equal(t, "cancel_command", commands[0].Type)
	})
}

func TestGuestCancellationCollisionDoesNotCommitIntent(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		svc := newGuestExecutionService(store, (&AgentService{store: store}).PublishCommand, nil)
		x := controlplane.GuestExecution{ID: "execution", AgentID: "agent", Topology: "lab", Node: "node", Status: "queued", Version: 1, CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveGuestExecution(ctx, x))
		cmd, err := persistence.GuestExecutionCancellationCommand(x)
		require.NoError(t, err)
		cmd.Operation.Node = "other"
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		_, err = svc.Cancel(ctx, x.ID)
		require.ErrorIs(t, err, persistence.ErrConflict)
		retained, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.False(t, retained.CancelRequested, "outbox collision must roll back cancellation intent")
		require.Equal(t, x.Version, retained.Version)
	})
}

func TestGuestCancellationCannotBypassCompoundCommit(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "execution", AgentID: "agent", Topology: "lab", Node: "node", Status: "queued", Version: 1, CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveGuestExecution(ctx, x))
		x.CancelRequested = true
		ok, err := store.CompareAndSwapGuestExecution(ctx, x, 1)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.False(t, ok)
		x.ID = "new-cancelled"
		require.ErrorIs(t, store.SaveGuestExecution(ctx, x), persistence.ErrConflict)
	})
}

func TestGuestCancellationSQLWriteFailureRollsBack(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		switch s := store.(type) {
		case *controlstore.SQLite:
			db, err := s.OpenDB()
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `CREATE TRIGGER reject_cancel BEFORE INSERT ON sysbox_agent_commands WHEN NEW.type='cancel_command' BEGIN SELECT RAISE(ABORT,'injected cancel outbox failure'); END`)
			require.NoError(t, err)
		case *controlstore.Postgres:
			pool, err := s.PoolFor(ctx)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `CREATE FUNCTION reject_cancel() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data->>'type' = 'cancel_command' THEN RAISE EXCEPTION 'injected cancel outbox failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_cancel BEFORE INSERT ON sysbox_agent_commands FOR EACH ROW EXECUTE FUNCTION reject_cancel()`)
			require.NoError(t, err)
		default:
			t.Skip("SQL transaction failure injection")
		}
		svc := newGuestExecutionService(store, nil, nil)
		x, err := svc.Create(ctx, "lab", "node", "agent", controlplane.GuestExecutionRequest{Argv: []string{"sleep", "30"}})
		require.NoError(t, err)
		_, err = svc.Cancel(ctx, x.ID)
		require.ErrorContains(t, err, "injected cancel outbox failure")
		got, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.False(t, got.CancelRequested)
		require.Equal(t, x.Version, got.Version)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
	})
}

func TestGuestCancellationFileRecoveryRevokesAfterTerminalAndRetirement(t *testing.T) {
	ctx := context.Background()
	store := &controlstore.Local{RunsDir: t.TempDir()}
	x := controlplane.GuestExecution{ID: "execution", AgentID: "agent", CommandID: "source", Topology: "lab", Node: "node", Status: "queued", Version: 1, CreatedAt: time.Now().UTC(), Request: controlplane.GuestExecutionRequest{Argv: []string{"private-command"}}}
	cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ExecutionRequest: x.Request, TopologyUID: "topology"}
	require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
	storage := controlstore.AuthorityStorage{Store: store}
	authority := persistence.NewExecutionGrantRepository(storage)
	session, err := authority.OpenSession(ctx, "agent", "host", "")
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: x.CommandID, ActionID: x.ID, Attempt: 1, Operation: "guest_task", Digest: "digest"}, time.Minute)
	require.NoError(t, err)
	before, err := storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	pending, err := store.CancelGuestExecution(ctx, x.ID)
	require.NoError(t, err)
	cancelCommand, err := persistence.GuestExecutionCancellationCommand(*pending)
	require.NoError(t, err)
	require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cancelCommand.ID))
	pending.Status = "failed"
	pending.EndedAt = time.Now().UTC()
	ok, err := store.CompareAndSwapGuestExecution(ctx, *pending, pending.Version)
	require.NoError(t, err)
	require.True(t, ok)
	terminal, err := store.GetGuestExecution(ctx, x.ID)
	require.NoError(t, err)
	terminal.Request = controlplane.GuestExecutionRequest{}
	ok, err = store.CompareAndSwapGuestExecution(ctx, *terminal, terminal.Version)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, store.DeleteAgentCommand(ctx, "agent", x.CommandID))
	seeds, err := persistence.NewFilesystemGuestRepository(store.RunsDir).FilesystemGuestDispatchCommands(ctx)
	require.NoError(t, err)
	require.Len(t, seeds, 2, "completion and redaction must preserve both immutable seeds")
	// Model a crash after the combined operation/outbox rename but before
	// authority publication: only the old authority file survives.
	require.NoError(t, storage.UpdateExecutionAuthority(ctx, func([]byte) ([]byte, error) { return before, nil }))
	cold := &controlstore.Local{RunsDir: store.RunsDir}
	require.NoError(t, (&AgentService{store: cold}).reconcileGuestExecutionCancellations(ctx))
	got, err := authority.InspectGrant(ctx, "agent", grant.Scope)
	require.NoError(t, err)
	require.Equal(t, controlplane.GrantRevoked, got.State)
	stored, err := cold.GetGuestExecution(ctx, x.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", stored.Status)
	commands, err := cold.ListAgentCommands(ctx, "agent")
	require.NoError(t, err)
	require.Empty(t, commands, "neither retired source nor cancellation may resurrect")
	require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"))
}

func TestGuestCancellationSQLAuthorityFailureRollsBackOutbox(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		if _, local := store.(*controlstore.Local); local {
			t.Skip("SQL rollback injection")
		}
		ctx := context.Background()
		storage := controlstore.AuthorityStorage{Store: store}
		require.NoError(t, persistence.NewExecutionGrantRepository(storage).ObserveTime(ctx))
		before, err := storage.ReadExecutionAuthority(ctx)
		require.NoError(t, err)
		svc := newGuestExecutionService(store, nil, nil)
		x, err := svc.Create(ctx, "lab", "node", "agent", controlplane.GuestExecutionRequest{Argv: []string{"sleep", "30"}})
		require.NoError(t, err)
		switch s := store.(type) {
		case *controlstore.SQLite:
			db, err := s.OpenDB()
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `CREATE TRIGGER reject_authority BEFORE UPDATE ON sysbox_execution_authority BEGIN SELECT RAISE(ABORT,'injected authority failure'); END`)
			require.NoError(t, err)
		case *controlstore.Postgres:
			pool, err := s.PoolFor(ctx)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `CREATE FUNCTION reject_authority() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected authority failure'; END $$; CREATE TRIGGER reject_authority BEFORE UPDATE ON sysbox_execution_authority FOR EACH ROW EXECUTE FUNCTION reject_authority()`)
			require.NoError(t, err)
		}
		_, err = svc.Cancel(ctx, x.ID)
		require.ErrorContains(t, err, "injected authority failure")
		got, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.False(t, got.CancelRequested)
		require.Equal(t, x.Version, got.Version)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
		after, err := storage.ReadExecutionAuthority(ctx)
		require.NoError(t, err)
		require.JSONEq(t, string(before), string(after))
	})
}
