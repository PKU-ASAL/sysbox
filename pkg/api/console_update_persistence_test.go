package api

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestConsoleUpdateDoesNotPublishFailedPersistence(t *testing.T) {
	hub := newConsoleSessionHub(&localAPIStore{runsDir: t.TempDir()})
	sess := createConsoleForTest(t, hub)
	hub.store = failingConsoleSave{apiStore: hub.store}
	err := hub.Update(sess.ID, func(s *controlplane.ConsoleSession) {
		s.Status = controlplane.ConsoleSessionStatusClosed
		s.Audit[0].Message = "uncommitted"
		s.Audit = append(s.Audit, controlplane.Event{Message: "new"})
	})
	require.Error(t, err)
	current, err := hub.Snapshot(sess.ID)
	require.NoError(t, err)
	require.Equal(t, sess.Status, current.Status)
	require.NotEqual(t, "uncommitted", current.Audit[0].Message)
	require.Len(t, current.Audit, len(sess.Audit))
	require.ErrorIs(t, hub.Update("missing", func(*controlplane.ConsoleSession) { t.Fatal("missing session callback") }), errConsoleSessionNotFound)
}

type failConsoleUpdateOnly struct {
	apiStore
	saves int
}

func (s *failConsoleUpdateOnly) CompareAndSwapConsoleSession(context.Context, controlplane.ConsoleSession, controlplane.ConsoleSession) error {
	return errors.New("private update storage failure")
}

func (s *failConsoleUpdateOnly) SaveConsoleSession(ctx context.Context, sess controlplane.ConsoleSession) error {
	s.saves++
	if s.saves > 1 {
		return errors.New("private update storage failure")
	}
	return s.apiStore.SaveConsoleSession(ctx, sess)
}

func TestConsoleDispatchRequiresAuditUpdatePersistence(t *testing.T) {
	s := guestExecutionTestServer(t)
	require.NoError(t, s.workspaceService().UpsertProject(context.Background(), "lab", map[string][]byte{"field.sysbox.hcl": []byte("resource \"sysbox_node\" \"web\" { substrate = \"docker\" }")}))
	s.consoles.store = &failConsoleUpdateOnly{apiStore: s.apiStore}
	s.consoleService().publishCommand = func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		t.Fatal("dispatch after failed update")
		return controlplane.AgentCommand{}, nil
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/topologies/lab/nodes/web/sessions", nil))
	require.Equal(t, 503, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "private update storage failure")
	items, err := s.apiStore.ListConsoleSessions(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, controlplane.ConsoleSessionStatusQueued, items[0].Status)
	require.Len(t, items[0].Audit, 1)
}

func TestConsoleCompletionPropagatesPersistenceError(t *testing.T) {
	hub := newConsoleSessionHub(&localAPIStore{runsDir: t.TempDir()})
	sess := createConsoleForTest(t, hub)
	hub.store = failingConsoleSave{apiStore: hub.store}
	svc := &AgentService{consoles: hub}
	err := svc.updateConsoleSessionFromCommandEvent(controlplane.AgentCommand{Type: "session_open", Session: &sess}, controlplane.AgentCommandEvent{Status: "completed"})
	require.Error(t, err)
	current, err := hub.Snapshot(sess.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.ConsoleSessionStatusQueued, current.Status)
}
