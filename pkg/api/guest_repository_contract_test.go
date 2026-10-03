package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

type guestMetadataStore interface {
	guestExecutionPersistence
	guestFileOperationPersistence
	guestAcceptancePersistence
}

func TestSQLiteGuestExecutionDispatchIsAtomic(t *testing.T) {
	s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db"), runsDir: t.TempDir()}
	db, err := s.open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	execution := controlplane.GuestExecution{ID: "exec-dispatch", Version: 1, Topology: "lab", Node: "web", AgentID: "agent", CommandID: "cmd-dispatch", Status: controlplane.GuestExecutionQueued, Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
	cmd := controlplane.AgentCommand{ID: "cmd-dispatch", AgentID: "agent", Type: "guest_execution", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, Execution: &controlplane.GuestExecution{ID: execution.ID, Topology: execution.Topology, Node: execution.Node, AgentID: execution.AgentID}, ExecutionRequest: execution.Request}
	require.NoError(t, s.CreateGuestExecutionDispatch(ctx, execution, cmd))
	got, err := s.GetGuestExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, cmd.ID, got.CommandID)
	commands, err := s.ListAgentCommands(ctx, "agent")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Equal(t, cmd.ID, commands[0].ID)
}

func eachGuestMetadataStore(t *testing.T, run func(*testing.T, guestMetadataStore)) {
	t.Helper()
	for _, kind := range []string{"local", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "local" {
				run(t, &localAPIStore{runsDir: t.TempDir()})
				return
			}
			s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db"), runsDir: t.TempDir()}
			db, err := s.open()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			run(t, s)
		})
	}
}

func TestGuestExecutionRepositoryGuardsTransitions(t *testing.T) {
	eachGuestMetadataStore(t, func(t *testing.T, s guestMetadataStore) {
		ctx := context.Background()
		initial := controlplane.GuestExecution{ID: "exec", Version: 1, Topology: "lab", Node: "web", AgentID: "agent", Status: controlplane.GuestExecutionQueued}
		require.NoError(t, s.SaveGuestExecution(ctx, initial))
		for _, mutate := range []func(*controlplane.GuestExecution){
			func(x *controlplane.GuestExecution) { x.AgentID = "other" },
			func(x *controlplane.GuestExecution) { x.Node = "other" },
			func(x *controlplane.GuestExecution) { x.Topology = "other" },
			func(x *controlplane.GuestExecution) { x.Status = "not-a-status" },
		} {
			changed := initial
			mutate(&changed)
			ok, err := s.CompareAndSwapGuestExecution(ctx, changed, 1)
			require.Error(t, err)
			require.False(t, ok)
		}
		done := initial
		done.Status = controlplane.GuestExecutionCompleted
		done.Result = controlplane.GuestExecutionResult{ExitCode: 7}
		ok, err := s.CompareAndSwapGuestExecution(ctx, done, 1)
		require.NoError(t, err)
		require.True(t, ok)
		require.ErrorIs(t, s.SaveGuestExecution(ctx, initial), persistence.ErrConflict, "unconditional save must not bypass CAS")
		initial.Version = 2
		ok, err = s.CompareAndSwapGuestExecution(ctx, initial, 2)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.False(t, ok)
		got, err := s.GetGuestExecution(ctx, initial.ID)
		require.NoError(t, err)
		require.Equal(t, int64(2), got.Version)
		require.Equal(t, 7, got.Result.ExitCode)
	})
}

func TestGuestFileRepositoryGuardsTransitions(t *testing.T) {
	eachGuestMetadataStore(t, func(t *testing.T, s guestMetadataStore) {
		ctx := context.Background()
		initial := controlplane.GuestFileOperation{ID: "file", Version: 1, Topology: "lab", Node: "web", AgentID: "agent", Status: controlplane.GuestExecutionQueued}
		require.NoError(t, s.SaveGuestFileOperation(ctx, initial))
		changed := initial
		changed.AgentID = "other"
		ok, err := s.CompareAndSwapGuestFileOperation(ctx, changed, 1)
		require.Error(t, err)
		require.False(t, ok)
		changed = initial
		changed.CancelRequested = true
		ok, err = s.CompareAndSwapGuestFileOperation(ctx, changed, 1)
		require.NoError(t, err)
		require.True(t, ok)
		initial.Version = 2
		ok, err = s.CompareAndSwapGuestFileOperation(ctx, initial, 2)
		require.ErrorIs(t, err, persistence.ErrConflict, "cancel evidence cannot be discarded")
		require.False(t, ok)
		got, err := s.GetGuestFileOperation(ctx, initial.ID)
		require.NoError(t, err)
		require.True(t, got.CancelRequested)
		require.Equal(t, int64(2), got.Version)
	})
}

func TestGuestRepositoryMissingAndInvalidKeys(t *testing.T) {
	eachGuestMetadataStore(t, func(t *testing.T, s guestMetadataStore) {
		ctx := context.Background()
		_, err := s.GetGuestExecution(ctx, "missing")
		require.ErrorIs(t, err, persistence.ErrNotFound)
		_, err = s.GetGuestFileOperation(ctx, "missing")
		require.ErrorIs(t, err, persistence.ErrNotFound)
		require.Error(t, s.SaveGuestExecution(ctx, controlplane.GuestExecution{ID: "../escape", Version: 1, Status: controlplane.GuestExecutionQueued}))
		require.Error(t, s.SaveGuestExecution(ctx, controlplane.GuestExecution{ID: "negative", Version: -1, Status: controlplane.GuestExecutionQueued}))
	})
}

func TestGuestExecutionEmptyEnvironmentDispatchBinding(t *testing.T) {
	eachGuestMetadataStore(t, func(t *testing.T, s guestMetadataStore) {
		publications := 0
		svc := newGuestExecutionService(s, func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error) {
			publications++
			return controlplane.AgentCommand{ID: "command"}, nil
		}, nil)
		x, err := svc.Create(context.Background(), "lab", "node", "agent", controlplane.GuestExecutionRequest{Argv: []string{"true"}, Environment: map[string]string{}})
		require.NoError(t, err, "JSON omitempty normalization must not invalidate an unchanged request after dispatch")
		require.Equal(t, 1, publications)
		stored, err := s.GetGuestExecution(context.Background(), x.ID)
		require.NoError(t, err)
		require.Equal(t, "command", stored.CommandID)
		require.Equal(t, int64(2), stored.Version)
	})
}
