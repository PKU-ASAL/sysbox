package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestConsoleExistingSessionRequiresCurrentConsoleRole(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.cfg.API.Console.AllowedRoles = []string{"console"}
	sess := createConsoleForTest(t, s.consoles)
	for _, request := range []struct{ method, suffix string }{{"GET", ""}, {"POST", "/cancel"}, {"GET", "/attach"}} {
		r := httptest.NewRequest(request.method, "/v1/sessions/"+sess.ID+request.suffix, nil)
		r.Header.Set("X-Sysbox-User", "viewer")
		r.Header.Set("X-Sysbox-Roles", "viewer")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		require.Equal(t, 403, w.Code, request.method+request.suffix+": "+w.Body.String())
	}
	stored, err := s.apiStore.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.True(t, persistence.SameConsoleSession(sess, *stored), "denied access cannot change the session or audit")
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, commands)
}

func TestConsoleAuthorizedCancellationAuditsCurrentSubject(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.cfg.API.Console.AllowedRoles = []string{"console"}
	sess := createConsoleForTest(t, s.consoles)
	r := httptest.NewRequest("POST", "/v1/sessions/"+sess.ID+"/cancel", nil)
	r.Header.Set("X-Sysbox-User", "operator")
	r.Header.Set("X-Sysbox-Roles", "console")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	stored, err := s.apiStore.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	audit := stored.Audit[len(stored.Audit)-1]
	require.Equal(t, "operator", audit.Actor)
	require.Equal(t, []string{"console"}, audit.Roles)
}

func TestConsoleRoleIsCheckedBeforeBrowserUpgrade(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.cfg.API.Console.AllowedRoles = []string{"console"}
	s.cfg.API.RBAC.AdminRoles = []string{"admin"}
	sess := createConsoleForTest(t, s.consoles)
	for _, role := range []string{"console", "admin"} {
		for _, suffix := range []string{"", "/attach"} {
			r := httptest.NewRequest("GET", "/v1/sessions/"+sess.ID+suffix, nil)
			r.Header.Set("X-Sysbox-User", "operator")
			r.Header.Set("X-Sysbox-Roles", role)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if suffix == "" {
				require.Equal(t, 200, w.Code, w.Body.String())
			} else {
				require.Equal(t, 426, w.Code, "authorized request reaches websocket upgrade validation")
			}
		}
	}
}

func TestConsoleMissingAuthorizationCannotCancel(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	sess := createConsoleForTest(t, s.consoles)
	service := &ConsoleService{consoles: s.consoles}
	_, err := service.Cancel(context.Background(), sess.ID, requestSubject{User: "operator"})
	require.Error(t, err)
	stored, err := s.consoles.Snapshot(sess.ID)
	require.NoError(t, err)
	require.True(t, persistence.SameConsoleSession(sess, stored))
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, commands)
}

func TestConsoleRegistryFailureIsUnavailableAndRedacted(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	sess := createConsoleForTest(t, s.consoles)
	s.consoles.initErr = errors.New("private registry connection details")
	for _, request := range []struct{ method, suffix string }{{"GET", ""}, {"POST", "/cancel"}, {"GET", "/attach"}} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(request.method, "/v1/sessions/"+sess.ID+request.suffix, nil))
		require.Equal(t, 503, w.Code)
		require.NotContains(t, w.Body.String(), "private")
	}
}
