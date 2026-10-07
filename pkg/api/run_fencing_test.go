package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func fencingServer(t *testing.T, backend string) *Server {
	t.Helper()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	if backend == "postgres" {
		cfg.State.Backend = isolatedPostgresTestDSN(t)
	} else {
		cfg.State.Backend = "sqlite://" + t.TempDir() + "/state.db"
	}
	return NewServerWithConfig(cfg)
}
func TestExpiredRunCannotRenewOrComplete(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := fencingServer(t, backend)
			ctx := context.Background()
			run := controlplane.Run{ID: "run", Topology: "topology", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "incarnation-one:claim-one", LeaseUntil: time.Now().Add(-time.Second), Attempt: 1}
			require.NoError(t, s.apiStore.SaveRun(ctx, run))
			_, ok, err := s.apiStore.RenewRunLease(ctx, run.ID, run.AgentID, run.LeaseOwner, time.Minute)
			require.NoError(t, err)
			require.False(t, ok, "expired owner must not revive itself")
			run.Status = controlplane.RunDone
			_, err = s.agentService().CompleteRun("agent", "run", controlplane.RunCompletion{Run: run})
			require.Error(t, err, "expired completion must not override authoritative state")
		})
	}
}
func TestCompletionRejectsDifferentIncarnation(t *testing.T) {
	s := fencingServer(t, "sqlite")
	ctx := context.Background()
	run := controlplane.Run{ID: "run", Topology: "topology", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "new-incarnation:new-claim", LeaseUntil: time.Now().Add(time.Minute), Attempt: 2}
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	run.LeaseOwner = "old-incarnation:old-claim"
	run.Attempt = 1
	run.Status = controlplane.RunDone
	_, err := s.agentService().CompleteRun("agent", "run", controlplane.RunCompletion{Run: run})
	require.Error(t, err)
	current, err := s.apiStore.GetRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, controlplane.RunRunning, current.Status)
}

func TestExpiredRunSettlesAcknowledgedCommandAfterAPIRestart(t *testing.T) {
	s := fencingServer(t, "sqlite")
	ctx := context.Background()
	run := controlplane.Run{ID: "run", Topology: "topo", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "old-process:claim", LeaseUntil: time.Now().Add(-time.Second), Attempt: 1}
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	require.NoError(t, s.apiStore.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "cmd", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusAcknowledged, Run: &run}))
	s.jobs.markExpiredLeases(time.Now())
	cmd, err := s.agentService().FindCommand(ctx, "agent", "cmd")
	require.NoError(t, err)
	got, terminal := s.agentService().ReconcileCommandState(ctx, cmd)
	require.True(t, terminal, "ACK must not retain command after run owner expires")
	require.Equal(t, controlplane.AgentCommandStatusFailed, got.Status)
}

func TestRenewalFencesStaleExpirySnapshot(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := fencingServer(t, backend)
			ctx := context.Background()
			before := controlplane.Run{ID: "run", Topology: "topo", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "session:claim", LeaseUntil: time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute), Attempt: 1}
			require.NoError(t, s.apiStore.SaveRun(ctx, before))
			_, ok, err := s.apiStore.RenewRunLease(ctx, before.ID, before.AgentID, before.LeaseOwner, time.Hour)
			require.NoError(t, err)
			require.True(t, ok)
			expired := before
			expired.MarkLeaseExpired(time.Now())
			ok, err = s.apiStore.CompareAndSwapRun(ctx, before, expired, false)
			require.NoError(t, err)
			require.False(t, ok)
			current, err := s.apiStore.GetRun(ctx, before.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.RunRunning, current.Status)
		})
	}
}
func TestConcurrentCompletionAndExpiryHaveOneWinner(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := fencingServer(t, backend)
			ctx := context.Background()
			before := controlplane.Run{ID: "run", Topology: "topo", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "session:claim", LeaseUntil: time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute), Attempt: 1}
			require.NoError(t, s.apiStore.SaveRun(ctx, before))
			done, expired := before, before
			done.Status = controlplane.RunDone
			expired.MarkLeaseExpired(time.Now())
			outcomes := make(chan bool, 2)
			errs := make(chan error, 2)
			for _, next := range []controlplane.Run{done, expired} {
				go func(next controlplane.Run) {
					var ok bool
					var err error
					for attempt := 0; attempt < 20; attempt++ {
						ok, err = s.apiStore.CompareAndSwapRun(ctx, before, next, false)
						if err == nil {
							break
						}
						time.Sleep(time.Millisecond)
					}
					outcomes <- ok
					errs <- err
				}(next)
			}
			wins := 0
			for range 2 {
				if <-outcomes {
					wins++
				}
				require.NoError(t, <-errs)
			}
			require.Equal(t, 1, wins)
			current, err := s.apiStore.GetRun(ctx, "run")
			require.NoError(t, err)
			require.True(t, current.Status.IsTerminal())
		})
	}
}

func TestUnknownGuestCommandTerminatesWithoutReplay(t *testing.T) {
	s := fencingServer(t, "sqlite")
	ctx := context.Background()
	old := time.Now().Add(-controlplane.AgentCommandExecutionLimit - time.Second)
	cmd := controlplane.AgentCommand{ID: "guest-command", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusRunning, AckedAt: old, LeaseOwner: "dead-stream"}
	require.NoError(t, s.apiStore.SaveAgentCommand(ctx, cmd))
	s.agentService().ReconcileLostCommands(ctx, time.Now())
	got, err := s.agentService().FindCommand(ctx, "agent", cmd.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.AgentCommandStatusFailed, got.Status)
	require.Contains(t, got.Err, "outcome unknown")
	require.False(t, got.IsPending(), "unknown external effects cannot be blindly reexecuted")
}

func TestCompletionReceiptRepairsProjectionAfterCrash(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := fencingServer(t, backend)
			ctx := context.Background()
			run := controlplane.Run{ID: "first", Topology: "topo", Workspace: "topo", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "session:claim", LeaseUntil: time.Now().Add(time.Minute), Attempt: 1}
			require.NoError(t, s.apiStore.SaveRun(ctx, run))
			run.Status = controlplane.RunDone
			body := controlplane.RunCompletion{Run: run, Projection: controlplane.Projection{Serial: 3, ResourceCount: 2}}
			receipt, err := s.agentService().CompleteRun("agent", run.ID, body)
			require.NoError(t, err)
			durable, err := s.apiStore.GetRun(ctx, run.ID)
			require.NoError(t, err)
			require.NotNil(t, durable.CompletionProjection)
			// Drop all volatile projection state, exactly as a crash after the commit does.
			s = NewServerWithConfig(s.cfg)
			body.Projection.Serial = 99
			body.Projection.ResourceCount = 99
			replay, err := s.agentService().CompleteRun("agent", run.ID, body)
			require.NoError(t, err)
			require.Equal(t, receipt.Projection, replay.Projection)
			require.Equal(t, []controlplane.Projection{receipt.Projection}, s.agents.ListProjections("agent"))
			newer := receipt.Projection
			newer.Serial = 4
			newer.UpdatedAt = newer.UpdatedAt.Add(time.Second)
			s.agents.SaveProjection(newer)
			_, err = s.agentService().CompleteRun("agent", run.ID, body)
			require.NoError(t, err)
			require.Equal(t, []controlplane.Projection{newer}, s.agents.ListProjections("agent"), "old completion cannot overwrite newer run view")
		})
	}
}
func TestCommandCASFencesRenewedDeliveryAndTerminalState(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := fencingServer(t, backend)
			ctx := context.Background()
			old := controlplane.AgentCommand{ID: "cmd", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusDelivered, LeaseOwner: "old-stream", Attempt: 1, LeaseUntil: time.Now().Add(-time.Second)}
			require.NoError(t, s.apiStore.SaveAgentCommand(ctx, old))
			newer, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "agent", "cmd", "new-stream", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			stale := old
			stale.Status = controlplane.AgentCommandStatusAcknowledged
			ok, err = s.apiStore.CompareAndSwapAgentCommand(ctx, old, stale)
			require.NoError(t, err)
			require.False(t, ok)
			terminal := *newer
			terminal.Status = controlplane.AgentCommandStatusFailed
			ok, err = s.apiStore.CompareAndSwapAgentCommand(ctx, *newer, terminal)
			require.NoError(t, err)
			require.True(t, ok)
			running := *newer
			running.Status = controlplane.AgentCommandStatusRunning
			ok, err = s.apiStore.CompareAndSwapAgentCommand(ctx, *newer, running)
			require.NoError(t, err)
			require.False(t, ok)
			got, err := s.agentService().FindCommand(ctx, "agent", "cmd")
			require.NoError(t, err)
			require.Equal(t, controlplane.AgentCommandStatusFailed, got.Status)
			require.Equal(t, "new-stream", got.LeaseOwner)
		})
	}
}

func TestSupervisorDoesNotFreshApplyAfterAmbiguousAgentLoss(t *testing.T) {
	s := fencingServer(t, "sqlite")
	failed := controlplane.Run{ID: "lost", Topology: "topo", Op: "apply", Status: controlplane.RunFailed, Recoverable: true, StartedAt: time.Now(), Err: "run lease expired"}
	require.NoError(t, s.apiStore.SaveRun(context.Background(), failed))
	s.jobs.cacheCommittedRun(&failed)
	supervisor := newSupervisor(s, time.Second)
	supervisor.policy = SupervisorPolicyRestartOnCrash
	snap := HealthSnapshot{Health: controlplane.TopologyHealth{Status: controlplane.ResourceHealthDrifted}}
	supervisor.maybeRepair("topo", &snap)
	require.Equal(t, "blocked_recovery_required", snap.Action)
	require.Empty(t, snap.RunID)
}

type lostCompletionReceiptStore struct{ apiStore }

func (s lostCompletionReceiptStore) CompareAndSwapRun(ctx context.Context, before, next controlplane.Run, live bool) (bool, error) {
	ok, err := s.apiStore.CompareAndSwapRun(ctx, before, next, live)
	if err == nil && ok && next.CompletionProjection != nil {
		return false, errors.New("connection lost after committed completion")
	}
	return ok, err
}
func TestCompletionCrashAfterCASBeforeProjectionIsRepairable(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := fencingServer(t, backend)
			ctx := context.Background()
			run := controlplane.Run{ID: "run", Topology: "topo", Workspace: "topo", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "process:claim", LeaseUntil: time.Now().Add(time.Minute), Attempt: 1}
			require.NoError(t, s.apiStore.SaveRun(ctx, run))
			run.Status = controlplane.RunDone
			body := controlplane.RunCompletion{Run: run, Projection: controlplane.Projection{Serial: 7, ResourceCount: 3}}
			s.agentService().store = lostCompletionReceiptStore{s.apiStore}
			_, err := s.agentService().CompleteRun("agent", run.ID, body)
			require.ErrorContains(t, err, "after committed completion")
			require.Empty(t, s.agents.ListProjections("agent"))
			restarted := NewServerWithConfig(s.cfg)
			require.NoError(t, restarted.agentService().restoreCompletionProjections(ctx))
			require.Len(t, restarted.agents.ListProjections("agent"), 1)
			body.Projection.Serial = 999
			replay, err := restarted.agentService().CompleteRun("agent", run.ID, body)
			require.NoError(t, err)
			require.Equal(t, int64(7), replay.Projection.Serial)
		})
	}
}
func TestConcurrentStaleAckAndDeliveryHaveOneOwner(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := fencingServer(t, backend)
			ctx := context.Background()
			old := controlplane.AgentCommand{ID: "cmd", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusDelivered, LeaseOwner: "old-stream", Attempt: 1, LeaseUntil: time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)}
			require.NoError(t, s.apiStore.SaveAgentCommand(ctx, old))
			ack := old
			ack.Status = controlplane.AgentCommandStatusAcknowledged
			outcomes := make(chan bool, 2)
			errs := make(chan error, 2)
			go func() { ok, err := s.apiStore.CompareAndSwapAgentCommand(ctx, old, ack); outcomes <- ok; errs <- err }()
			go func() {
				var ok bool
				var err error
				for n := 0; n < 20; n++ {
					_, ok, err = s.apiStore.AcquireAgentCommandLease(ctx, "agent", "cmd", "new-stream", time.Minute)
					if err == nil {
						break
					}
					time.Sleep(time.Millisecond)
				}
				outcomes <- ok
				errs <- err
			}()
			wins := 0
			for range 2 {
				if <-outcomes {
					wins++
				}
				require.NoError(t, <-errs)
			}
			require.Equal(t, 1, wins)
		})
	}
}

func TestRetainedHistoryCannotHidePendingOrRecoverableCommands(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := fencingServer(t, backend)
			ctx := context.Background()
			now := time.Now()
			pending := controlplane.AgentCommand{ID: "old-pending", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusQueued, CreatedAt: now.Add(-time.Hour)}
			require.NoError(t, s.apiStore.SaveAgentCommand(ctx, pending))
			for i := 0; i < 520; i++ {
				require.NoError(t, s.apiStore.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: fmt.Sprintf("history-%04d", i), AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusCompleted, CreatedAt: now}))
			}
			got, err := s.apiStore.PendingAgentCommands(ctx, "agent")
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, pending.ID, got[0].ID)
			_, err = s.agentService().FindCommand(ctx, "agent", pending.ID)
			require.NoError(t, err)
			zombie := controlplane.AgentCommand{ID: "zz-unknown", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusRunning, AckedAt: now.Add(-time.Hour)}
			require.NoError(t, s.apiStore.SaveAgentCommand(ctx, zombie))
			for range 5 {
				s.agentService().ReconcileLostCommands(ctx, now)
			}
			recovered, err := s.agentService().FindCommand(ctx, "agent", zombie.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.AgentCommandStatusFailed, recovered.Status)
		})
	}
}

func TestRecoveryReportAccountsForInterruptedEffect(t *testing.T) {
	s := fencingServer(t, "sqlite")
	ctx := context.Background()
	labels := map[string]string{runtime.LabelManaged: "true", runtime.LabelTopology: "topo", runtime.LabelResource: "sysbox_node.web", runtime.LabelRunID: "run"}
	cp := runtime.OperationCheckpoint{RunID: "run", Topology: "topo", Steps: []runtime.OperationStep{{Kind: "resource", Resource: "sysbox_node.web", Action: controlplane.PlanActionCreate, Status: runtime.OperationStarted, Labels: labels}}}
	require.NoError(t, s.apiStore.SaveCheckpoint(ctx, "topo", "run", cp))
	mgr := state.NewManager(filepath.Join(t.TempDir(), "state.json"))
	report, err := recoverCheckpoint(ctx, s.apiStore, "topo", "run", mgr, "recovery-owner")
	require.NoError(t, err)
	require.Empty(t, report.Recovered)
	require.Len(t, report.Skipped, 1)
	require.Equal(t, "blocked_unconfirmed_effect", report.Skipped[0].Status)
	require.Equal(t, labels, report.Skipped[0].Labels)
}

func TestPostgresRenewalUsesAuthoritativeLiveLeaseColumn(t *testing.T) {
	s := fencingServer(t, "postgres")
	ctx := context.Background()
	run := controlplane.Run{ID: "run", Topology: "topo", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "owner", Attempt: 1, LeaseUntil: time.Now().Add(time.Minute)}
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	store := s.apiStore.(*postgresAPIStore)
	conn, err := store.connect(ctx)
	require.NoError(t, err)
	// Keep the JSON snapshot intentionally stale to ensure only the database
	// lease column can authorize this write, not an earlier Go clock check.
	_, err = conn.Exec(ctx, `UPDATE sysbox_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id='run'`)
	require.NoError(t, err)
	conn.Release()
	_, ok, err := store.RenewRunLease(ctx, "run", "agent", "owner", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
}
func TestSQLiteCompletionInsertChecksDatabaseClock(t *testing.T) {
	s := fencingServer(t, "sqlite")
	store := s.apiStore.(*sqliteAPIStore)
	ctx := context.Background()
	db, err := store.open()
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	written, err := insertSQLiteRunChecked(ctx, tx, controlplane.Run{ID: "run", Topology: "topo", Status: controlplane.RunDone, LeaseUntil: time.Now().Add(-time.Second)}, true)
	require.NoError(t, err)
	require.False(t, written, "write-time lease test must reject an expired receipt")
}

func TestAgentExecutionBridgeLeavesCanonicalCompletionToAPI(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			api := fencingServer(t, backend)
			ctx := context.Background()
			require.NoError(t, api.agentService().Save(ctx, controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline}))
			original := controlplane.Run{ID: "agent-run", Topology: "topo", Workspace: "topo", AgentID: "agent", Status: controlplane.RunAssigned}
			require.NoError(t, api.apiStore.SaveRun(ctx, original))
			claimed, ok, err := api.apiStore.ClaimRun(ctx, original.ID, "agent", "process:claim", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			// This is the exact bridge wired by `sysbox agent start`: it opens the same
			// state backend as the API, unlike the CLI-only LocalBridge.
			agentBridge := NewExecutionBridge(api.cfg)
			agentBridge.AttachRun(claimed)
			agentBridge.Finish(claimed, nil)
			canonical, err := api.apiStore.GetRun(ctx, original.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.RunRunning, canonical.Status, "agent must not bypass API owner CAS with SaveRun")
			require.Nil(t, canonical.CompletionProjection)
			body, err := json.Marshal(controlplane.RunCompletion{Run: *claimed, Projection: controlplane.Projection{Serial: 1, ResourceCount: 2}})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			api.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/agents/agent/runs/agent-run/complete", bytes.NewReader(body)))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			canonical, err = api.apiStore.GetRun(ctx, original.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.RunDone, canonical.Status)
			require.NotNil(t, canonical.CompletionProjection)
		})
	}
}
