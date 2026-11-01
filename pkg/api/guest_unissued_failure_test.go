package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestUnissuedGuestFailureDoesNotRequireUserCancellation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "execution", CommandID: "source", AgentID: "agent", Topology: "lab", Node: "node", Status: "queued", Version: 1, CreatedAt: time.Now().UTC()}
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ActionID: x.ID, TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
		require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
		_, err := newGuestExecutionService(store, nil, nil).MarkRunning(ctx, x.ID)
		require.NoError(t, err)
		cmd.Status = controlplane.AgentCommandStatusFailed
		cmd.EndedAt = time.Now().UTC()
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.NoError(t, (&AgentService{store: store}).reconcileUnissuedGuestFailures(ctx))
		got, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, "failed", got.Status)
		require.Equal(t, "not_started", got.ResultClass)
		require.False(t, got.CancelRequested)
		require.NoError(t, (&AgentService{store: store}).reconcileUnissuedGuestFailures(ctx))
		repeated, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, got, repeated)
	})
}

func TestSupervisorClosesUnissuedGuestFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := NewServer(t.TempDir(), t.TempDir())
	x, cmd := admitUnissuedGuest(t, s.apiStore)
	cmd.Status = "failed"
	cmd.EndedAt = time.Now().UTC()
	require.NoError(t, s.apiStore.SaveAgentCommand(ctx, cmd))
	newSupervisor(s, time.Minute).Scan(ctx)
	got, err := s.apiStore.GetGuestExecution(ctx, x.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.False(t, got.CancelRequested)
}

func TestUnissuedGuestFailureKeepsOrderedTimesAcrossAPIClockSkew(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "execution", CommandID: "source", AgentID: "agent", Topology: "lab", Node: "node", Status: "queued", Version: 1, CreatedAt: time.Now().UTC().Add(time.Minute)}
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ActionID: x.ID, TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
		require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
		cmd.Status = "failed"
		cmd.EndedAt = time.Now().UTC()
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.NoError(t, (&AgentService{store: store}).reconcileUnissuedGuestFailures(ctx))
		got, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.False(t, got.EndedAt.Before(got.CreatedAt), "failover clock skew must not leave terminal activity unresolved forever")
	})
}

func TestGuestFailureRecoverySurvivesMutationWriteFailure(t *testing.T) {
	for _, issued := range []bool{false, true} {
		name := "never-issued"
		if issued {
			name = "issued"
		}
		t.Run(name, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
				ctx := context.Background()
				x, cmd := admitUnissuedGuest(t, store)
				authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
				var grant *controlplane.ExecutionGrant
				if issued {
					session, err := authority.OpenSession(ctx, x.AgentID, "host", "")
					require.NoError(t, err)
					grant, err = authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: 1}, AgentID: x.AgentID, SessionID: session.ID, CommandID: cmd.ID, ActionID: x.ID, Attempt: 1, Operation: "guest_task", Digest: "digest"}, time.Minute)
					require.NoError(t, err)
				}
				cmd.Status = "failed"
				cmd.EndedAt = time.Now().UTC()
				require.NoError(t, store.SaveAgentCommand(ctx, cmd))
				require.NoError(t, store.DeleteAgentCommand(ctx, x.AgentID, cmd.ID))
				var restore func()
				switch dbstore := store.(type) {
				case *controlstore.Local:
					if os.Geteuid() == 0 {
						t.Skip("permission fault requires unprivileged test process")
					}
					directory := filepath.Join(dbstore.RunsDir, "_guest-executions")
					if issued {
						directory = filepath.Join(dbstore.RunsDir, "_execution-authority")
					}
					require.NoError(t, os.Chmod(directory, 0500))
					restore = func() { require.NoError(t, os.Chmod(directory, 0700)) }
					t.Cleanup(restore)
				case *controlstore.SQLite:
					db, err := dbstore.OpenDB()
					require.NoError(t, err)
					table, when := "sysbox_guest_executions", ""
					if issued {
						table = "sysbox_execution_authority"
						when = ` WHEN CAST(NEW.data AS TEXT) LIKE '%"revoked"%'`
					}
					_, err = db.ExecContext(ctx, "CREATE TRIGGER reject_guest_failure BEFORE UPDATE ON "+table+when+" BEGIN SELECT RAISE(ABORT,'injected guest failure'); END")
					require.NoError(t, err)
					restore = func() { _, err := db.ExecContext(ctx, "DROP TRIGGER reject_guest_failure"); require.NoError(t, err) }
				case *controlstore.Postgres:
					pool, err := dbstore.PoolFor(ctx)
					require.NoError(t, err)
					table, check := "sysbox_guest_executions", "TRUE"
					if issued {
						table = "sysbox_execution_authority"
						check = `NEW.data::text LIKE '%"revoked"%'`
					}
					_, err = pool.Exec(ctx, "CREATE FUNCTION reject_guest_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF "+check+" THEN RAISE EXCEPTION 'injected guest failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_guest_failure BEFORE UPDATE ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_guest_failure()")
					require.NoError(t, err)
					restore = func() {
						_, err := pool.Exec(ctx, "DROP TRIGGER reject_guest_failure ON "+table)
						require.NoError(t, err)
					}
				}
				require.Error(t, (&AgentService{store: store}).reconcileUnissuedGuestFailures(ctx))
				pending, err := store.GetGuestExecution(ctx, x.ID)
				require.NoError(t, err)
				require.Equal(t, "queued", pending.Status)
				restore()
				require.NoError(t, (&AgentService{store: store}).reconcileUnissuedGuestFailures(ctx))
				final, err := store.GetGuestExecution(ctx, x.ID)
				require.NoError(t, err)
				if issued {
					require.Equal(t, "queued", final.Status)
					held, err := authority.InspectGrant(ctx, x.AgentID, grant.Scope)
					require.NoError(t, err)
					require.Equal(t, controlplane.GrantRevoked, held.State)
				} else {
					require.Equal(t, "failed", final.Status)
					require.Equal(t, "not_started", final.ResultClass)
				}
			})
		})
	}
}

func admitUnissuedGuest(t *testing.T, store controlstore.Store) (controlplane.GuestExecution, controlplane.AgentCommand) {
	t.Helper()
	x := controlplane.GuestExecution{ID: "execution", CommandID: "source", AgentID: "agent", Topology: "lab", Node: "node", Status: "queued", Version: 1, CreatedAt: time.Now().UTC()}
	cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ActionID: x.ID, TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
	require.NoError(t, store.CreateGuestExecutionDispatch(context.Background(), x, cmd))
	return x, cmd
}

func TestUnissuedGuestFailureRecoversRetirementWindow(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		x, cmd := admitUnissuedGuest(t, store)
		cmd.Status = "failed"
		cmd.EndedAt = time.Now().UTC()
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		_, err := store.FailUnissuedGuestExecution(ctx, x.ID)
		require.ErrorIs(t, err, persistence.ErrConflict, "live delivery is not a no-execution proof")
		require.NoError(t, store.DeleteAgentCommand(ctx, x.AgentID, cmd.ID)) // crash before execution update
		require.NoError(t, (&AgentService{store: store}).reconcileUnissuedGuestFailures(ctx))
		got, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, "failed", got.Status)
		require.False(t, got.CancelRequested)
		commands, err := store.ListAgentCommands(ctx, x.AgentID)
		require.NoError(t, err)
		require.Empty(t, commands)
	})
}

func TestGuestDeliveryFailureWithAuthorityWaitsForHost(t *testing.T) {
	for _, history := range []string{"active", "stopped"} {
		t.Run(history, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
				ctx := context.Background()
				x, cmd := admitUnissuedGuest(t, store)
				authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
				session, err := authority.OpenSession(ctx, x.AgentID, "host", "")
				require.NoError(t, err)
				request := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: 1}, AgentID: x.AgentID, SessionID: session.ID, CommandID: cmd.ID, ActionID: x.ID, Attempt: 1, Operation: "guest_task", Digest: "digest"}
				grant, err := authority.Issue(ctx, request, time.Minute)
				require.NoError(t, err)
				var successor *controlplane.ExecutionGrant
				if history == "stopped" {
					require.NoError(t, authority.AcceptHostStop(ctx, *grant, "stop"))
					request.CommandID = "successor"
					request.ActionID = "successor"
					successor, err = authority.Issue(ctx, request, time.Minute)
					require.NoError(t, err)
				}
				cmd.Status = "failed"
				cmd.EndedAt = time.Now().UTC()
				require.NoError(t, store.SaveAgentCommand(ctx, cmd))
				require.NoError(t, (&AgentService{store: store}).reconcileUnissuedGuestFailures(ctx))
				got, err := store.GetGuestExecution(ctx, x.ID)
				require.NoError(t, err)
				require.Equal(t, "queued", got.Status)
				require.True(t, got.EndedAt.IsZero())
				require.False(t, got.CancelRequested)
				if successor != nil {
					require.NoError(t, authority.Authorize(ctx, *successor))
				} else {
					held, err := authority.InspectGrant(ctx, x.AgentID, grant.Scope)
					require.NoError(t, err)
					require.Equal(t, controlplane.GrantRevoked, held.State)
				}
			})
		})
	}
}

func TestUnissuedFailureCannotOverrideCancellationOrLiveDelivery(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		x, cmd := admitUnissuedGuest(t, store)
		require.NoError(t, (&AgentService{store: store}).reconcileUnissuedGuestFailures(ctx))
		got, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, "queued", got.Status)
		cmd.Status = "failed"
		cmd.EndedAt = time.Now().UTC()
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.NoError(t, store.DeleteAgentCommand(ctx, x.AgentID, cmd.ID))
		cancelled, err := store.CancelGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		_, err = store.FailUnissuedGuestExecution(ctx, x.ID)
		require.ErrorIs(t, err, persistence.ErrConflict)
		retained, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, cancelled, retained)
	})
}
