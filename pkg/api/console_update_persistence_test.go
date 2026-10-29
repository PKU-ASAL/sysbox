package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/stretchr/testify/require"
)

func TestConsoleUpdateDoesNotPublishFailedPersistence(t *testing.T) {
	hub := newConsoleSessionHub(&controlstore.Local{RunsDir: t.TempDir()})
	sess := createConsoleForTest(t, hub)
	hub.store = failingConsoleSave{Store: hub.store}
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
	require.ErrorIs(t, hub.Update("missing", func(*controlplane.ConsoleSession) { t.Fatal("missing session callback") }), controlstore.ErrConsoleSessionNotFound)
}

type failConsoleAdmission struct {
	controlstore.Store
	attempted controlplane.ConsoleSession
}

func (s *failConsoleAdmission) CreateConsoleDispatch(_ context.Context, sess controlplane.ConsoleSession, _ controlplane.AgentCommand) error {
	s.attempted = sess
	return errors.New("private update storage failure")
}

func TestConsoleDispatchRequiresAuditPersistence(t *testing.T) {
	s := consoleAdmissionTestServer(t)
	failure := &failConsoleAdmission{Store: s.apiStore}
	s.consoles.store = failure
	s.consoleService().wakeCommand = func(string, controlplane.AgentCommand) error {
		t.Fatal("dispatch after failed update")
		return nil
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/topologies/lab/nodes/web/sessions", nil))
	require.Equal(t, 503, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "private update storage failure")
	items, err := s.apiStore.ListConsoleSessions(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, items)
	require.Len(t, failure.attempted.Audit, 2)
	require.Equal(t, "allow", failure.attempted.Audit[1].Action)
}

func TestConsoleCompletionPropagatesPersistenceError(t *testing.T) {
	hub := newConsoleSessionHub(&controlstore.Local{RunsDir: t.TempDir()})
	sess := createConsoleForTest(t, hub)
	hub.store = failingConsoleSave{Store: hub.store}
	svc := &AgentService{consoles: hub}
	err := svc.updateConsoleSessionFromCommandEvent(controlplane.AgentCommand{Type: "session_open", Session: &sess}, controlplane.AgentCommandEvent{Status: "completed"})
	require.Error(t, err)
	current, err := hub.Snapshot(sess.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.ConsoleSessionStatusQueued, current.Status)
}
