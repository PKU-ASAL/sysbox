package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func createConsoleForTest(t *testing.T, hub *consoleSessionHub) controlplane.ConsoleSession {
	t.Helper()
	sess, err := hub.Create(context.Background(), "lab", "node", "agent", controlplane.ConsoleRequest{})
	require.NoError(t, err)
	return sess
}

func TestConsoleCreateStorageFailurePreventsDispatch(t *testing.T) {
	s := guestExecutionTestServer(t)
	require.NoError(t, s.workspaceService().UpsertProject(context.Background(), "lab", map[string][]byte{"field.sysbox.hcl": []byte("resource \"sysbox_node\" \"web\" { substrate = \"docker\" }")}))
	s.consoles.store = failingConsoleSave{apiStore: s.apiStore}
	s.consoleService().publishCommand = func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		t.Fatal("session_open dispatched without durable session")
		return controlplane.AgentCommand{}, nil
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/topologies/lab/nodes/web/sessions", nil))
	require.Equal(t, 503, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "storage unavailable")
	require.Empty(t, s.consoles.sessions)
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, commands)
}

func TestConsoleCreateRequiresDurableRecord(t *testing.T) {
	hub := newConsoleSessionHub(&localAPIStore{runsDir: t.TempDir()})
	hub.store = failingConsoleSave{apiStore: hub.store}
	session, err := hub.Create(context.Background(), "lab", "node", "agent", controlplane.ConsoleRequest{})
	require.Error(t, err)
	require.Empty(t, session.ID)
	require.Empty(t, hub.sessions, "failed persistence must not publish a usable in-memory session")
}

func TestConsoleCreateRejectsCancelledContextAndMissingStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hub := newConsoleSessionHub(&localAPIStore{runsDir: t.TempDir()})
	_, err := hub.Create(ctx, "lab", "node", "agent", controlplane.ConsoleRequest{})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, hub.sessions)
	_, err = newConsoleSessionHub(nil).Create(context.Background(), "lab", "node", "agent", controlplane.ConsoleRequest{})
	require.Error(t, err)
}

func TestConsoleCreatePersistsBeforeItCanBeDiscovered(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		hub := newConsoleSessionHub(store)
		session, err := hub.Create(context.Background(), "lab", "node", "agent", controlplane.ConsoleRequest{})
		require.NoError(t, err)
		stored, err := store.GetConsoleSession(context.Background(), session.ID)
		require.NoError(t, err)
		require.Equal(t, session.ID, stored.ID)
		require.Equal(t, session.Topology, stored.Topology)
		require.Equal(t, session.Node, stored.Node)
		require.Equal(t, session.AgentID, stored.AgentID)
		require.Equal(t, session.Status, stored.Status)
		require.True(t, session.CreatedAt.Equal(stored.CreatedAt))
		_, err = hub.Get(session.ID)
		require.NoError(t, err)
	})
}
