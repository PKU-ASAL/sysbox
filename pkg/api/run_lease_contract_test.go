package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func eachRunLeaseStore(t *testing.T, test func(*testing.T, apiStore)) {
	t.Helper()
	for _, kind := range []string{"local", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "postgres" {
				s := &postgresAPIStore{dsn: isolatedPostgresTestDSN(t)}
				pool, err := s.poolFor(context.Background())
				require.NoError(t, err)
				t.Cleanup(pool.Close)
				test(t, s)
				return
			}
			if kind == "local" {
				test(t, &localAPIStore{runsDir: t.TempDir()})
				return
			}
			s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "runs.db"), runsDir: t.TempDir()}
			db, err := s.open()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			test(t, s)
		})
	}
}
func TestRunLeaseRejectsInvalidTTLAndExpiredDeadline(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
		require.NoError(t, s.SaveRun(ctx, run))
		for _, ttl := range []time.Duration{0, -time.Second} {
			_, ok, err := s.ClaimRun(ctx, run.ID, run.AgentID, "worker", ttl)
			require.Error(t, err)
			require.False(t, ok)
		}
		run.ID = "deadline"
		run.DeadlineAt = time.Now().Add(-time.Minute)
		require.NoError(t, s.SaveRun(ctx, run))
		got, ok, err := s.ClaimRun(ctx, run.ID, run.AgentID, "worker", time.Minute)
		require.NoError(t, err)
		require.False(t, ok)
		require.Equal(t, controlplane.RunAssigned, got.Status)
	})
}
func TestRunLeaseCannotRenewExpiredOrShortenLiveLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		expired := controlplane.Run{ID: "expired", Topology: "lab", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "worker", LeaseUntil: time.Now().Add(-time.Minute)}
		require.NoError(t, s.SaveRun(ctx, expired))
		_, ok, err := s.RenewRunLease(ctx, expired.ID, expired.AgentID, expired.LeaseOwner, time.Minute)
		require.NoError(t, err)
		require.False(t, ok)
		live := expired
		live.ID = "live"
		live.LeaseUntil = time.Now().UTC().Add(time.Hour)
		require.NoError(t, s.SaveRun(ctx, live))
		got, ok, err := s.RenewRunLease(ctx, live.ID, live.AgentID, live.LeaseOwner, time.Second)
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, got.LeaseUntil.Equal(live.LeaseUntil), "renewal must not shorten the existing lease")
	})
}
func TestRunLeaseUsesLatestCompleteSnapshot(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		old := controlplane.Run{ID: "history", Topology: "lab", AgentID: "old-agent", Status: controlplane.RunQueued, Revision: "old"}
		require.NoError(t, s.SaveRun(ctx, old))
		latest := old
		latest.Status = controlplane.RunAssigned
		latest.AgentID = "agent"
		latest.Revision = "pinned"
		latest.SnapshotPath = "/immutable/field.sysbox.hcl"
		latest.DeadlineAt = time.Now().UTC().Add(time.Hour)
		require.NoError(t, s.SaveRun(ctx, latest))
		got, ok, err := s.ClaimRun(ctx, latest.ID, latest.AgentID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, latest.Revision, got.Revision)
		require.Equal(t, latest.SnapshotPath, got.SnapshotPath)
		require.True(t, latest.DeadlineAt.Equal(got.DeadlineAt))
		require.Equal(t, 1, got.Attempt)
	})
}

type staleRunScan struct {
	apiStore
	snapshot controlplane.Run
}

func (s staleRunScan) LoadRuns(context.Context) ([]controlplane.Run, error) {
	return []controlplane.Run{s.snapshot}, nil
}
func TestRunLeaseExpiryDoesNotOverwriteConcurrentRenewal(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		now := time.Now().UTC()
		live := controlplane.Run{ID: "renewed", Topology: "lab", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "worker", LeaseUntil: now.Add(time.Hour)}
		require.NoError(t, s.SaveRun(context.Background(), live))
		stale := live
		stale.LeaseUntil = now.Add(-time.Second)
		jobs := newJobsWithPolicy(t.TempDir(), staleRunScan{apiStore: s, snapshot: stale}, false, time.Minute, "fail_recoverable")
		jobs.markExpiredLeases(now)
		stored, err := s.GetRun(context.Background(), live.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunRunning, stored.Status)
		require.True(t, live.LeaseUntil.Equal(stored.LeaseUntil))
	})
}

type declinedRunClaim struct{ apiStore }

func (s declinedRunClaim) ClaimRun(context.Context, string, string, string, time.Duration) (*controlplane.Run, bool, error) {
	return nil, false, nil
}
func TestRunClaimCannotFallBackToMemoryAuthorization(t *testing.T) {
	s := &localAPIStore{runsDir: t.TempDir()}
	initial := controlplane.Run{ID: "declined", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
	require.NoError(t, s.SaveRun(context.Background(), initial))
	j := newJobsWithRecovery(s.runsDir, declinedRunClaim{apiStore: s}, false)
	_, err := j.claim(initial.ID, initial.AgentID)
	require.Error(t, err)
	stored, err := s.GetRun(context.Background(), initial.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunAssigned, stored.Status)
}

func TestRunSaveCannotBypassLeaseOwnershipOrTerminalState(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		initial := controlplane.Run{ID: "protected", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
		require.NoError(t, s.SaveRun(ctx, initial))
		claimed, ok, err := s.ClaimRun(ctx, initial.ID, initial.AgentID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		for _, mutate := range []func(*controlplane.Run){func(r *controlplane.Run) { r.LeaseOwner = "other" }, func(r *controlplane.Run) { r.Attempt++ }, func(r *controlplane.Run) { r.AgentID = "other" }, func(r *controlplane.Run) { r.Status = controlplane.RunAssigned }} {
			bad := *claimed
			mutate(&bad)
			require.ErrorIs(t, s.SaveRun(ctx, bad), persistence.ErrConflict)
		}
		expired, ok, err := s.ExpireRunLease(ctx, initial.ID, claimed.LeaseUntil.Add(time.Second))
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, controlplane.RunFailed, expired.Status)
		late := *claimed
		late.Status = controlplane.RunDone
		require.ErrorIs(t, s.SaveRun(ctx, late), persistence.ErrConflict)
		stored, err := s.GetRun(ctx, initial.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunFailed, stored.Status)
	})
}
func TestRunDispatchPreservesDeadlineAndSnapshot(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := controlplane.Run{ID: "dispatch-deadline", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned, DeadlineAt: time.Now().UTC().Add(-time.Minute), SnapshotPath: "/pinned/field.sysbox.hcl"}
		_, created, err := s.CreateRunDispatch(ctx, RunDispatchRequest{Run: run, Fingerprint: "fingerprint", Command: controlplane.AgentCommand{ID: "command", AgentID: run.AgentID, Status: controlplane.AgentCommandStatusQueued, Type: "run_assigned", Run: &run}})
		require.NoError(t, err)
		require.True(t, created)
		got, err := s.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.Equal(t, run.SnapshotPath, got.SnapshotPath)
		require.True(t, run.DeadlineAt.Equal(got.DeadlineAt))
		_, ok, err := s.ClaimRun(ctx, run.ID, run.AgentID, "worker", time.Minute)
		require.NoError(t, err)
		require.False(t, ok)
	})
}

func TestRunDispatchAttachesCommandToExistingAssignedRun(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := controlplane.Run{ID: "assigned-existing", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
		require.NoError(t, s.SaveRun(ctx, run))
		cmd := controlplane.AgentCommand{ID: "run-assigned-existing", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, Run: &run}
		_, created, err := s.CreateRunDispatch(ctx, RunDispatchRequest{Run: run, Command: cmd, Fingerprint: "run-dispatch:assigned-existing"})
		require.NoError(t, err)
		require.False(t, created)
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, cmd.ID, commands[0].ID)
	})
}
func TestLateRunCompletionCannotPublishRejectedState(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	run := s.jobs.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent"})
	s.jobs.assign(run, "agent")
	claimed, err := s.jobs.claim(run.ID, "agent")
	require.NoError(t, err)
	_, ok, err := s.apiStore.ExpireRunLease(ctx, run.ID, claimed.LeaseUntil.Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	late := *claimed
	late.Status = controlplane.RunDone
	late.EndedAt = time.Now().UTC()
	_, err = s.agentService().CompleteRun("agent", run.ID, controlplane.RunCompletion{Run: late})
	require.ErrorIs(t, err, persistence.ErrConflict)
	got, exists := s.jobs.get(run.ID)
	require.True(t, exists)
	require.Equal(t, controlplane.RunFailed, got.Status)
}

func TestRunRepositoryRejectsMalformedIdentity(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		for _, bad := range []controlplane.Run{{ID: "bad/key", Topology: "lab", Status: controlplane.RunQueued}, {ID: "alias", Topology: "lab", Status: controlplane.RunQueued, Operation: "apply", Op: "destroy"}} {
			require.Error(t, s.SaveRun(context.Background(), bad))
		}
	})
}
func TestRunRestartRejectsOldIncarnationWrites(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		initial := controlplane.Run{ID: "repeat", Topology: "lab", Operation: "apply", OperationKey: "request", AgentID: "agent", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC().Add(-time.Second)}
		require.NoError(t, s.SaveRun(ctx, initial))
		old, ok, err := s.ClaimRun(ctx, initial.ID, initial.AgentID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		old.Status = controlplane.RunDone
		old.EndedAt = time.Now().UTC().Add(time.Hour)
		require.NoError(t, s.SaveRun(ctx, *old))
		next := initial
		next.Status = controlplane.RunQueued
		next.QueuedAt = time.Now().UTC()
		next.StartedAt = next.QueuedAt
		next.LeaseOwner = "sysbox-api"
		require.ErrorIs(t, s.SaveRun(ctx, next), persistence.ErrConflict)
		restarted, err := s.RestartRun(ctx, next)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunQueued, restarted.Status)
		stored, err := s.GetRun(ctx, next.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunQueued, stored.Status)
		reloaded := newJobsWithRecovery(t.TempDir(), s, false)
		cached, exists := reloaded.get(next.ID)
		require.True(t, exists)
		require.Equal(t, controlplane.RunQueued, cached.Status)
		require.ErrorIs(t, s.SaveRun(ctx, *old), persistence.ErrConflict)
		_, ok, err = s.RenewRunLease(ctx, old.ID, old.AgentID, old.LeaseOwner, time.Minute)
		require.NoError(t, err)
		require.False(t, ok)
	})
}

func TestRunClaimDoesNotAttachInputsFromPreviousRound(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		current := controlplane.Run{ID: "round", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
		require.NoError(t, s.SaveRun(ctx, current))
		jobs := newJobsWithRecovery(t.TempDir(), s, false)
		old := current
		old.QueuedAt = current.QueuedAt.Add(-time.Hour)
		old.Inputs = map[string]string{"SECRET": "old-round"}
		jobs.remember(&old)
		claimed, err := jobs.claim(current.ID, current.AgentID)
		require.NoError(t, err)
		require.Empty(t, claimed.Inputs)
	})
}
func TestOldCompletionCannotFinishUnclaimedNewRound(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent"})
	require.NoError(t, s.jobs.assign(run, "agent"))
	report := controlplane.Run{ID: run.ID, AgentID: "agent", Status: controlplane.RunDone, LeaseOwner: "old-worker", Attempt: 1}
	_, err := s.agentService().CompleteRun("agent", run.ID, controlplane.RunCompletion{Run: report})
	require.Error(t, err)
	report.LeaseOwner = ""
	report.Attempt = 0
	_, err = s.agentService().CompleteRun("agent", run.ID, controlplane.RunCompletion{Run: report})
	require.Error(t, err, "omitting old proof cannot authorize a new round")
}
func TestRunClaimRejectsLostApplyInputs(t *testing.T) {
	s := &localAPIStore{runsDir: t.TempDir()}
	jobs := newJobsWithRecovery(s.runsDir, s, false)
	inputs := map[string]string{"flag": "transient-secret"}
	revision := "revision"
	run := jobs.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent", Revision: revision, OperationKey: applyOperationKey(revision, inputs, false), Inputs: inputs})
	require.NoError(t, jobs.assign(run, "agent"))
	reloaded := newJobsWithRecovery(s.runsDir, s, false)
	_, err := reloaded.claim(run.ID, "agent")
	require.Error(t, err)
	stored, err := s.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunAssigned, stored.Status)
	claimed, err := jobs.claim(run.ID, "agent")
	require.NoError(t, err)
	require.Equal(t, inputs, claimed.Inputs)
}

func TestRunCoalescingUsesDurableCurrentRound(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		a := newJobsWithRecovery(t.TempDir(), s, false)
		b := newJobsWithRecovery(t.TempDir(), s, false)
		opts := runStartOptions{OperationKey: "coalesce", AgentID: "agent"}
		old := a.startWithOptions("lab", "apply", opts)
		require.NoError(t, a.assign(old, "agent"))
		claimed, err := a.claim(old.ID, "agent")
		require.NoError(t, err)
		require.NoError(t, a.finish(claimed, nil))
		b.remember(claimed)
		next := newRun("lab", "apply", opts)
		require.NoError(t, a.forceStart(next))
		require.NoError(t, a.assign(next, "agent"))
		got, created, err := b.startWithResult("lab", "apply", opts)
		require.NoError(t, err)
		require.False(t, created)
		require.Equal(t, controlplane.RunAssigned, got.Status)
		require.True(t, got.QueuedAt.Equal(next.QueuedAt))
	})
}
func TestRemoteRunStatusSurvivesAPIReconstruction(t *testing.T) {
	s := &localAPIStore{runsDir: t.TempDir()}
	j := newJobsWithRecovery(s.runsDir, s, false)
	run := j.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent"})
	require.NoError(t, j.assign(run, "agent"))
	_, err := j.claim(run.ID, "agent")
	require.NoError(t, err)
	reloaded := newJobsWithRecovery(s.runsDir, s, true)
	got, ok := reloaded.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunRunning, got.Status)
}

func TestStatusReadPreservesInputsForCurrentRound(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		j := newJobsWithRecovery(t.TempDir(), s, false)
		inputs := map[string]string{"SECRET": "current-round"}
		run := j.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent", Inputs: inputs})
		require.NoError(t, j.assign(run, "agent"))
		_, exists := j.get(run.ID)
		require.True(t, exists)
		claimed, err := j.claim(run.ID, "agent")
		require.NoError(t, err)
		require.Equal(t, inputs, claimed.Inputs)
	})
}

type afterRunClaim struct {
	apiStore
	after func()
}

func (s *afterRunClaim) ClaimRun(ctx context.Context, id, agent, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	run, ok, err := s.apiStore.ClaimRun(ctx, id, agent, owner, ttl)
	if err == nil && ok && s.after != nil {
		s.after()
	}
	return run, ok, err
}
func TestRunClaimReturnsValidatedInputsDespiteCacheRefresh(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		wrapped := &afterRunClaim{apiStore: s}
		jobs := newJobsWithRecovery(t.TempDir(), wrapped, false)
		inputs := map[string]string{"SECRET": "validated"}
		revision := "revision"
		run := jobs.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent", Revision: revision, OperationKey: applyOperationKey(revision, inputs, false), Inputs: inputs})
		require.NoError(t, jobs.assign(run, "agent"))
		stale := *run
		wrapped.after = func() { require.ErrorIs(t, jobs.persist(&stale), persistence.ErrConflict) }
		claimed, err := jobs.claim(run.ID, "agent")
		require.NoError(t, err)
		require.Equal(t, inputs, claimed.Inputs)
	})
}
func TestSQLiteRunRejectsConflictingIndexedSnapshot(t *testing.T) {
	for _, column := range []string{"queued_at", "op", "operation_key"} {
		t.Run(column, func(t *testing.T) {
			s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "runs.db")}
			run := controlplane.Run{ID: "record", Topology: "lab", Op: "apply", Status: controlplane.RunQueued, QueuedAt: time.Now().UTC()}
			require.NoError(t, s.SaveRun(context.Background(), run))
			db, err := s.open()
			require.NoError(t, err)
			defer db.Close()
			value := "different"
			if column == "queued_at" {
				value = formatSQLiteTime(run.QueuedAt.Add(time.Hour))
			}
			_, err = db.Exec("UPDATE sysbox_runs SET "+column+"=? WHERE id=?", value, run.ID)
			require.NoError(t, err)
			_, err = s.GetRun(context.Background(), run.ID)
			require.ErrorIs(t, err, persistence.ErrCorruptRecord)
		})
	}
}
func TestRunCompletionCannotPublishForeignProjection(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent"})
	require.NoError(t, s.jobs.assign(run, "agent"))
	claimed, err := s.jobs.claim(run.ID, "agent")
	require.NoError(t, err)
	finished := *claimed
	finished.Status = controlplane.RunDone
	_, err = s.agentService().CompleteRun("agent", run.ID, controlplane.RunCompletion{Run: finished, Projection: controlplane.Projection{AgentID: "other", Topology: "foreign"}})
	require.Error(t, err)
	stored, err := s.apiStore.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunRunning, stored.Status)
}
