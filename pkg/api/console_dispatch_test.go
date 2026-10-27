package api

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func consoleDispatchFixture() (controlplane.ConsoleSession, controlplane.AgentCommand) {
	tty := true
	sess := controlplane.ConsoleSession{ID: "session", AgentID: "agent", Topology: "lab", Workspace: "workspace", Node: "node", Status: "queued", CreatedAt: time.Now().UTC(), TTY: tty, RequestedBy: "operator", Roles: []string{"console"}, Policy: "console.rbac"}
	req := controlplane.ConsoleRequest{Shell: "/bin/sh", Cmd: []string{"sh", "-c", "pwd"}, Env: map[string]string{"TERM": "xterm"}, WorkDir: "/tmp", TTY: &tty, Cols: 120, Rows: 32, TimeoutSeconds: 600, RequestedBy: sess.RequestedBy, Roles: sess.Roles, Policy: sess.Policy}
	cmd := persistence.DurableCommand(controlplane.AgentCommand{ID: "console-open", AgentID: sess.AgentID, Type: "session_open", Session: &sess, Request: req, CreatedAt: sess.CreatedAt})
	return sess, cmd
}

func TestConsoleDispatchPublishesCompleteOutboxAndPreservesRetirement(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		sess, cmd := consoleDispatchFixture()
		require.NoError(t, store.CreateConsoleDispatch(ctx, sess, cmd))
		stored, err := store.GetConsoleSession(ctx, sess.ID)
		require.NoError(t, err)
		require.True(t, persistence.SameConsoleSession(sess, *stored))
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, cmd.ID, commands[0].ID)
		require.Equal(t, cmd.Request, commands[0].Request)
		hub := newConsoleSessionHub(store)
		require.NoError(t, hub.initErr)
		require.NoError(t, hub.Cancel(context.Background(), sess.ID, "stop", requestSubject{User: "user"}))
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 2, "cancel must preserve the initial outbox record and atomically add cancellation")
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID))
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", persistence.ConsoleCancellationCommand(sess).ID))
		require.NoError(t, hub.Update(sess.ID, func(s *controlplane.ConsoleSession) { s.Err = "still awaiting verified stop" }))
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands, "retired delivery must not resurrect from its seed")
		require.ErrorIs(t, store.CreateConsoleDispatch(ctx, sess, cmd), persistence.ErrConflict)
	})
}

func TestConsoleDispatchConflictAndFreezePublishNeitherRecord(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		sess, cmd := consoleDispatchFixture()
		prior := cmd
		prior.Type, prior.Session = "inventory", nil
		require.NoError(t, store.SaveAgentCommand(ctx, prior))
		require.Error(t, store.CreateConsoleDispatch(ctx, sess, cmd))
		items, err := store.ListConsoleSessions(ctx, "")
		require.NoError(t, err)
		require.Empty(t, items)
		cmd.ID = "new-command"
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		_, err = authority.BeginMaintenance(ctx, "freeze", uid, "plan")
		require.NoError(t, err)
		require.ErrorIs(t, store.CreateConsoleDispatch(ctx, sess, cmd), persistence.ErrConflict)
		items, err = store.ListConsoleSessions(ctx, "")
		require.NoError(t, err)
		require.Empty(t, items)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, prior.ID, commands[0].ID)
	})
}

func TestConsoleAdmissionSurvivesNotificationFailure(t *testing.T) {
	s := consoleAdmissionTestServer(t)
	woke := false
	s.consoleService().wakeCommand = func(_ string, cmd controlplane.AgentCommand) error {
		woke = true
		_, err := s.apiStore.GetConsoleSession(context.Background(), cmd.Session.ID)
		require.NoError(t, err, "wake must follow commit")
		commands, err := s.apiStore.ListAgentCommands(context.Background(), "")
		require.NoError(t, err)
		require.Len(t, commands, 1, "outbox must already be durable at wake")
		return errors.New("notification unavailable")
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/topologies/lab/nodes/web/sessions", nil))
	require.Equal(t, 202, rec.Code, rec.Body.String())
	require.True(t, woke)
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	items, err := s.apiStore.ListConsoleSessions(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, items[0].ID, commands[0].Session.ID)
	require.Len(t, items[0].Audit, 2, "creation and authorization must commit with dispatch")
}

func TestConsoleOpenRejectsStandalonePublication(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		_, cmd := consoleDispatchFixture()
		publisher := &AgentService{store: store}
		_, err := publisher.PublishCommand(context.Background(), cmd.AgentID, cmd)
		require.ErrorIs(t, err, persistence.ErrInvalidRecord)
		commands, err := store.ListAgentCommands(context.Background(), "")
		require.NoError(t, err)
		require.Empty(t, commands)
	})
}

func TestConsoleAdmissionDoesNotAuthorizeRequestBodyIdentity(t *testing.T) {
	s := consoleAdmissionTestServer(t)
	s.cfg.API.Console.AllowedRoles = []string{"console"}
	req := httptest.NewRequest("POST", "/v1/topologies/lab/nodes/web/sessions", bytes.NewBufferString(`{"requested_by":"administrator","roles":["console"]}`))
	req.Header.Set("X-Sysbox-User", "alice")
	req.Header.Set("X-Sysbox-Roles", "viewer")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, 403, rec.Code, rec.Body.String())
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, commands)
	items, err := s.apiStore.ListConsoleSessions(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "alice", items[0].RequestedBy)
	require.Equal(t, []string{"viewer"}, items[0].Roles)
}

func TestConsoleDispatchRejectsInvalidBindingWithoutPublication(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		for name, mutate := range map[string]func(*controlplane.AgentCommand){
			"agent":   func(c *controlplane.AgentCommand) { c.AgentID = "other" },
			"session": func(c *controlplane.AgentCommand) { copy := *c.Session; copy.Node = "other"; c.Session = &copy },
			"type":    func(c *controlplane.AgentCommand) { c.Type = "inventory" },
			"status":  func(c *controlplane.AgentCommand) { c.Status = "running" },
			"attempt": func(c *controlplane.AgentCommand) { c.Attempt = 1 },
			"lease":   func(c *controlplane.AgentCommand) { c.LeaseUntil = time.Now().Add(time.Minute) },
		} {
			sess, cmd := consoleDispatchFixture()
			mutate(&cmd)
			require.ErrorIs(t, store.CreateConsoleDispatch(context.Background(), sess, cmd), persistence.ErrInvalidRecord, name)
		}
		sessions, err := store.ListConsoleSessions(context.Background(), "")
		require.NoError(t, err)
		require.Empty(t, sessions)
		commands, err := store.ListAgentCommands(context.Background(), "")
		require.NoError(t, err)
		require.Empty(t, commands)
	})
}

func TestConsoleDispatchConcurrentCommandCollisionHasOneWinner(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		for _, id := range []string{"one", "two"} {
			workers.Add(1)
			go func(id string) {
				defer workers.Done()
				sess, cmd := consoleDispatchFixture()
				sess.ID = id
				cmd.Session = &sess
				<-start
				results <- store.CreateConsoleDispatch(context.Background(), sess, cmd)
			}(id)
		}
		close(start)
		workers.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			}
		}
		require.Equal(t, 1, wins)
		sessions, err := store.ListConsoleSessions(context.Background(), "")
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		commands, err := store.ListAgentCommands(context.Background(), "")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, sessions[0].ID, commands[0].Session.ID)
	})
}
