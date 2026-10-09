package api

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestMetadataDeleteRechecksRunAndConsoleInsideCommit(t *testing.T) {
	for _, kind := range []string{"run", "console"} {
		t.Run(kind, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
				ctx := context.Background()
				s := stateDeleteTestServer(t, store)
				repo, err := s.actionRepository("lab")
				require.NoError(t, err)
				before, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				require.NoError(t, s.workspaceService().ensureTopologyQuiescent(ctx, "lab"))
				// Admission after preflight must still be visible at commit.
				run := controlplane.Run{ID: "late-run", Topology: "lab", Status: controlplane.RunQueued, QueuedAt: time.Now().UTC()}
				sess := controlplane.ConsoleSession{ID: "late-session", Topology: "lab", Workspace: "different-workspace", Status: "lost", CreatedAt: time.Now().UTC()}
				if kind == "run" {
					require.NoError(t, store.SaveRun(ctx, run))
				} else {
					require.NoError(t, store.SaveConsoleSession(ctx, sess))
				}
				require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), persistence.ErrConflict)
				after, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				require.Equal(t, before.TopologyUID, after.TopologyUID)
				require.Equal(t, before.Meta.Serial, after.Meta.Serial)
				if kind == "run" {
					run.Status, run.EndedAt = controlplane.RunFailed, time.Now().UTC()
					require.NoError(t, store.SaveRun(ctx, run))
					other := run
					other.ID, other.Topology, other.Status, other.EndedAt = "other", "other", controlplane.RunQueued, time.Time{}
					require.NoError(t, store.SaveRun(ctx, other))
				} else {
					closed := sess
					closed.Status, closed.EndedAt = "closed", time.Now().UTC()
					require.NoError(t, store.CompareAndSwapConsoleSession(ctx, sess, closed))
					sess.ID, sess.Topology = "other", "other"
					require.NoError(t, store.SaveConsoleSession(ctx, sess))
				}
				require.NoError(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), "terminal latest observations and unrelated activity do not block")
			})
		})
	}
}

func TestRunActivityRequiresValidTerminalEvidence(t *testing.T) {
	now := time.Now().UTC()
	for _, status := range []controlplane.RunStatus{controlplane.RunQueued, controlplane.RunAssigned, controlplane.RunRunning, "unknown", ""} {
		require.True(t, runActivityUnresolved(controlplane.Run{Status: status, QueuedAt: now, EndedAt: now}))
	}
	for _, status := range []controlplane.RunStatus{controlplane.RunDone, controlplane.RunFailed, controlplane.RunCancelled} {
		require.False(t, runActivityUnresolved(controlplane.Run{Status: status, QueuedAt: now, EndedAt: now}))
		require.True(t, runActivityUnresolved(controlplane.Run{Status: status, EndedAt: now}))
		require.True(t, runActivityUnresolved(controlplane.Run{Status: status, QueuedAt: now}))
		require.True(t, runActivityUnresolved(controlplane.Run{Status: status, QueuedAt: now, EndedAt: now.Add(-time.Second)}))
	}
}

func TestMetadataDeleteRejectsSQLiteRunIndexMismatch(t *testing.T) {
	ctx := context.Background()
	store := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db"), runsDir: t.TempDir()}
	db, err := store.open()
	require.NoError(t, err)
	defer db.Close()
	s := stateDeleteTestServer(t, store)
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	before, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	run := controlplane.Run{ID: "run", Topology: "lab", Status: controlplane.RunDone, QueuedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}
	require.NoError(t, store.SaveRun(ctx, run))
	require.NoError(t, s.workspaceService().ensureTopologyQuiescent(ctx, "lab"))
	_, err = db.ExecContext(ctx, "UPDATE sysbox_runs SET status='running' WHERE id='run'")
	require.NoError(t, err)
	_, err = store.LoadRuns(ctx)
	require.ErrorIs(t, err, persistence.ErrCorruptRecord)
	require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), persistence.ErrCorruptRecord)
	after, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, before.TopologyUID, after.TopologyUID)
	require.Equal(t, before.Meta.Serial, after.Meta.Serial)
}

func TestMetadataDeleteRechecksCommandsInsideCommit(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		before, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.NoError(t, s.workspaceService().ensureTopologyQuiescent(ctx, "lab"))
		cmd := controlplane.AgentCommand{ID: "late-command", AgentID: "unregistered", TopologyUID: before.TopologyUID, Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), persistence.ErrConflict)
		require.NoError(t, store.DeleteAgentCommand(ctx, cmd.AgentID, cmd.ID))
		cmd.ID, cmd.TopologyUID = "missing-uid", ""
		cmd.Run = &controlplane.Run{Topology: "lab"}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), persistence.ErrConflict)
		after, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, before.TopologyUID, after.TopologyUID)
		require.Equal(t, before.Meta.Serial, after.Meta.Serial)
		require.NoError(t, store.DeleteAgentCommand(ctx, cmd.AgentID, cmd.ID))
		cmd.ID, cmd.TopologyUID = "foreign", "other-incarnation"
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.NoError(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID))
	})
}

func TestCommandPublicationWaitsForAuthorityTransaction(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		done := make(chan error, 1)
		go func() {
			done <- (apiExecutionGrantStorage{store: store}).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
				memory := &grantTransaction{raw: raw}
				err := persistence.NewExecutionGrantRepository(memory).ObserveTime(ctx)
				close(entered)
				<-release
				return memory.raw, err
			})
		}()
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("authority transaction failed before exclusion: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("authority transaction did not acquire exclusion")
		}
		deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "inventory"}
		err := store.SaveAgentCommand(deadline, cmd)
		unblock()
		require.NoError(t, <-done)
		require.Error(t, err, "publication cannot pass the authority transaction")
		commands, err := store.ListAgentCommands(ctx, "")
		require.NoError(t, err)
		require.Empty(t, commands)
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
	})
}
