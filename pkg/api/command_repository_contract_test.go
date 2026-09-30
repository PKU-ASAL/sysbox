package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestCommandRepositoryDispatchRejectsExistingCommandID(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		require.NoError(t, s.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "reserved-command", AgentID: "agent", Type: "node_operation"}))
		run := controlplane.Run{ID: "new-run", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
		_, created, err := s.CreateRunDispatch(ctx, RunDispatchRequest{Run: run, Command: controlplane.AgentCommand{ID: "reserved-command", AgentID: "agent", Type: "run_assigned", Run: &run}, Fingerprint: "new"})
		require.Error(t, err)
		require.False(t, created)
		_, err = s.GetRun(ctx, run.ID)
		require.ErrorIs(t, err, persistence.ErrNotFound)
	})
}
func TestSQLiteCommandRepositoryRejectsCorruptSnapshot(t *testing.T) {
	for _, mutation := range []string{"record_json=X'6e756c6c'", "record_json=X'5b5d'", "lease_until='not-a-date'", "attempt=99", "execution_payload=X'7b'"} {
		t.Run(mutation, func(t *testing.T) {
			s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "commands.db")}
			ctx := context.Background()
			require.NoError(t, s.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation"}))
			db, err := s.open()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			_, err = db.ExecContext(ctx, "UPDATE sysbox_agent_commands SET "+mutation+" WHERE id='command'")
			require.NoError(t, err)
			_, err = s.ListAgentCommands(ctx, "agent")
			require.ErrorIs(t, err, persistence.ErrCorruptRecord)
			_, ok, err := s.AcquireAgentCommandLease(ctx, "agent", "command", "worker", time.Minute)
			require.ErrorIs(t, err, persistence.ErrCorruptRecord)
			require.False(t, ok)
		})
	}
}

func TestCommandRepositoryRoundTripsNonUTCTimestamps(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		stamp := time.Date(2026, 9, 30, 16, 0, 0, 123456789, time.FixedZone("local", 8*60*60))
		cmd := controlplane.AgentCommand{ID: "timezone", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusCompleted, CreatedAt: stamp, Delivered: stamp, AckedAt: stamp, EndedAt: stamp, LeaseOwner: "worker", LeaseUntil: stamp, Attempt: 1}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		got, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.True(t, stamp.Equal(got[0].CreatedAt))
		require.True(t, stamp.Equal(got[0].LeaseUntil))
		require.True(t, stamp.Equal(got[0].EndedAt))
	})
}

func TestLocalCommandRepositoryIgnoresUnrelatedStandaloneCorruption(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()
	require.NoError(t, store.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "mine", AgentID: "healthy", Type: "node_operation"}))
	path := filepath.Join(store.runsDir, "_agents", "broken", "commands", "bad.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0600))
	got, err := store.ListAgentCommands(ctx, "healthy")
	require.NoError(t, err)
	require.Len(t, got, 1)
	_, ok, err := store.AcquireAgentCommandLease(ctx, "healthy", "mine", "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = store.ListAgentCommands(ctx, "")
	require.Error(t, err, "unfiltered inventory must still detect corruption")
}

func TestCommandRepositoryExistingRunDispatchDoesNotCreateCommand(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		original := controlplane.Run{ID: "original", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
		first := RunDispatchRequest{Run: original, Command: controlplane.AgentCommand{ID: "shared-command", AgentID: "agent", Type: "run_assigned", Run: &original}, Fingerprint: "original"}
		_, _, err := s.CreateRunDispatch(ctx, first)
		require.NoError(t, err)
		existing := original
		existing.ID = "existing"
		require.NoError(t, s.SaveRun(ctx, existing))
		_, created, err := s.CreateRunDispatch(ctx, RunDispatchRequest{Run: existing, Command: first.Command, Fingerprint: "existing"})
		require.NoError(t, err)
		require.False(t, created)
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, original.ID, commands[0].Run.ID)
	})
}

func TestCommandRepositoryPreservesImmutablePayload(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued, Run: &controlplane.Run{ID: "run", Topology: "lab", Revision: "pinned", Status: controlplane.RunAssigned}}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		other := cmd
		changed := *cmd.Run
		changed.Revision = "different"
		other.Run = &changed
		require.ErrorIs(t, s.SaveAgentCommand(ctx, other), persistence.ErrConflict)
		other = cmd
		other.Type = "node_operation"
		require.ErrorIs(t, s.SaveAgentCommand(ctx, other), persistence.ErrConflict)
		got, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "pinned", got[0].Run.Revision)
	})
}

func TestCommandRepositoryRetirementPreventsDispatchResurrection(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := controlplane.Run{ID: "bundled", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
		cmd := controlplane.AgentCommand{ID: "bundle-command", AgentID: "agent", Type: "run_assigned", Run: &run}
		request := RunDispatchRequest{Run: run, Command: cmd, Fingerprint: "request"}
		_, created, err := s.CreateRunDispatch(ctx, request)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, s.DeleteAgentCommand(ctx, cmd.AgentID, cmd.ID))
		_, created, err = s.CreateRunDispatch(ctx, request)
		require.NoError(t, err)
		require.False(t, created)
		commands, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
		require.ErrorIs(t, s.SaveAgentCommand(ctx, cmd), persistence.ErrConflict)
		_, ok, err := s.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		require.False(t, ok)
	})
}
func TestCommandRepositoryConcurrentLeaseHasSingleWinner(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		cmd := controlplane.AgentCommand{ID: "contended", AgentID: "agent", Type: "run_assigned"}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		type result struct {
			ok  bool
			err error
		}
		results := make(chan result, 12)
		start := make(chan struct{})
		for i := 0; i < 12; i++ {
			go func() {
				<-start
				_, ok, err := s.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Hour)
				results <- result{ok, err}
			}()
		}
		close(start)
		winners := 0
		for i := 0; i < 12; i++ {
			r := <-results
			require.NoError(t, r.err)
			if r.ok {
				winners++
			}
		}
		require.Equal(t, 1, winners)
	})
}
func TestCommandRepositoryRejectsStaleLeaseWriter(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		old := controlplane.AgentCommand{ID: "stale", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusLeased, LeaseOwner: "old", LeaseUntil: time.Now().Add(-time.Minute), Attempt: 1}
		require.NoError(t, s.SaveAgentCommand(ctx, old))
		current, ok, err := s.AcquireAgentCommandLease(ctx, "agent", old.ID, "new", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		old.MarkDelivered(time.Now())
		require.ErrorIs(t, s.SaveAgentCommand(ctx, old), persistence.ErrConflict)
		// A status-only cancellation preserves the current lease and original payload.
		require.NoError(t, s.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: old.ID, AgentID: "agent", Status: controlplane.AgentCommandStatusCancelled}))
		got, err := s.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, current.LeaseOwner, got[0].LeaseOwner)
		require.Equal(t, current.Attempt, got[0].Attempt)
		require.Equal(t, old.Type, got[0].Type)
	})
}

type failingCommandSave struct {
	apiStore
	failure error
}

func (s failingCommandSave) PrepareAgentCommandDelivery(context.Context, controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	return nil, s.failure
}
func TestCommandDeliveryRejectsCancelledLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		cmd := controlplane.AgentCommand{ID: "cancel-before-send", AgentID: "agent", Type: "node_operation"}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, store.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: cmd.ID, AgentID: "agent", Status: controlplane.AgentCommandStatusCancelled}))
		stream := AgentStreamService{store: store}
		require.ErrorIs(t, stream.writeCommand(ctx, nil, deliverAgentCommand(*leased)), persistence.ErrConflict)
	})
}
func TestCommandDeliveryStopsBeforeSocketWriteOnPersistenceFailure(t *testing.T) {
	failure := errors.New("write unavailable")
	service := AgentStreamService{store: failingCommandSave{failure: failure}}
	require.ErrorIs(t, service.writeCommand(context.Background(), nil, controlplane.AgentCommand{ID: "command", AgentID: "agent"}), failure)
}
func TestCommandRepositoryRetriesExpiredDeliveryLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		cmd := controlplane.AgentCommand{ID: "expired", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusLeased, LeaseOwner: "old", LeaseUntil: time.Now().UTC().Add(-time.Minute), Attempt: 1}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		got, ok, err := s.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "new", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, 2, got.Attempt)
		require.Equal(t, "new", got.LeaseOwner)
	})
}
func TestCommandRepositoryRejectsInvalidLeaseAndReplacement(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		cmd := controlplane.AgentCommand{ID: "lease", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued}
		require.NoError(t, s.SaveAgentCommand(ctx, cmd))
		_, ok, err := s.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "owner", 0)
		require.Error(t, err)
		require.False(t, ok)
		claimed, ok, err := s.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "owner", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		changed := *claimed
		changed.LeaseOwner = "foreign"
		require.ErrorIs(t, s.SaveAgentCommand(ctx, changed), persistence.ErrConflict)
	})
}
