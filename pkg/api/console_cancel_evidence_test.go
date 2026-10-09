package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestConsoleCancellationRequestDoesNotProveStop(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	hub := newConsoleSessionHub(store)
	sess := createConsoleForTest(t, hub)
	hub.Update(sess.ID, func(s *controlplane.ConsoleSession) {
		s.Status = controlplane.ConsoleSessionStatusRunning
		s.StartedAt = time.Now().UTC()
	})
	require.NoError(t, hub.Cancel(sess.ID, "requested", "user"))
	current, err := store.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelling", current.Status)
	require.True(t, current.EndedAt.IsZero())
	require.True(t, consoleActivityUnresolved(*current))
	require.NoError(t, hub.Cancel(sess.ID, "retry", "user"))
	again, err := store.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, len(current.Audit), len(again.Audit), "retry must not append another cancellation event")
	_ = newConsoleSessionHub(store)
	recovered, err := store.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.True(t, consoleActivityUnresolved(*recovered), "restart cannot turn cancel request into completion")
}

func TestConsoleCancelHTTPDistinguishesUnavailableFromMissing(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	sess := createConsoleForTest(t, s.consoles)
	s.consoleSvc = &ConsoleService{consoles: s.consoles, publishCommand: func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		return controlplane.AgentCommand{}, errors.New("private connection details")
	}}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/sessions/"+sess.ID+"/cancel", nil))
	require.Equal(t, 503, rec.Code)
	require.NotContains(t, rec.Body.String(), "private connection details")
	missing := httptest.NewRecorder()
	s.ServeHTTP(missing, httptest.NewRequest("POST", "/v1/sessions/absent/cancel", nil))
	require.Equal(t, 404, missing.Code)
	s.consoles.store = failingConsoleSave{apiStore: s.apiStore}
	s.consoleSvc.publishCommand = func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		t.Fatal("failed persistence must not dispatch")
		return controlplane.AgentCommand{}, nil
	}
	storageFailure := httptest.NewRecorder()
	s.ServeHTTP(storageFailure, httptest.NewRequest("POST", "/v1/sessions/"+sess.ID+"/cancel", nil))
	require.Equal(t, 503, storageFailure.Code)
	require.NotContains(t, storageFailure.Body.String(), "storage unavailable")
}

type delayedConsoleSave struct {
	apiStore
	entered, release chan struct{}
	once             sync.Once
}

func (s *delayedConsoleSave) SaveConsoleSession(ctx context.Context, x controlplane.ConsoleSession) error {
	if x.Status == controlplane.ConsoleSessionStatusRunning {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	return s.apiStore.SaveConsoleSession(ctx, x)
}

func TestConsoleCancellationCannotBeOverwrittenByEarlierUpdate(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	hub := newConsoleSessionHub(store)
	sess := createConsoleForTest(t, hub)
	delay := &delayedConsoleSave{apiStore: store, entered: make(chan struct{}), release: make(chan struct{})}
	hub.store = delay
	var once sync.Once
	release := func() { once.Do(func() { close(delay.release) }) }
	defer release()
	updated := make(chan struct{})
	go func() {
		defer close(updated)
		hub.Update(sess.ID, func(s *controlplane.ConsoleSession) { s.Status = controlplane.ConsoleSessionStatusRunning })
	}()
	<-delay.entered
	cancelled := make(chan error, 1)
	go func() { cancelled <- hub.Cancel(sess.ID, "requested", "user") }()
	select {
	case err := <-cancelled:
		t.Fatalf("cancellation overtook pending persistence: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	<-updated
	require.NoError(t, <-cancelled)
	current, err := store.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.ConsoleSessionStatusCancelling, current.Status)
	copy, err := hub.Snapshot(sess.ID)
	require.NoError(t, err)
	copy.Audit[0].Message = "mutated"
	again, err := hub.Snapshot(sess.ID)
	require.NoError(t, err)
	require.NotEqual(t, "mutated", again.Audit[0].Message)
}

type failingConsoleSave struct{ apiStore }

func (s failingConsoleSave) SaveConsoleSession(context.Context, controlplane.ConsoleSession) error {
	return errors.New("storage unavailable")
}

func TestConsoleCancelDoesNotAcknowledgeFailedPersistence(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	hub := newConsoleSessionHub(store)
	sess := createConsoleForTest(t, hub)
	hub.store = failingConsoleSave{apiStore: store}
	require.Error(t, hub.Cancel(sess.ID, "requested", "user"))
	current, err := hub.Snapshot(sess.ID)
	require.NoError(t, err)
	require.Equal(t, sess.Status, current.Status)
	require.Len(t, current.Audit, len(sess.Audit))
	st, err := hub.Get(sess.ID)
	require.NoError(t, err)
	select {
	case <-st.cancel:
		t.Fatal("uncommitted cancellation signalled")
	default:
	}
}

func TestConsoleTransportLossCannotManufactureCompletion(t *testing.T) {
	for _, status := range []string{"queued", "running", "cancelling", "lost", "closed", "failed", "cancelled", "denied"} {
		t.Run(status, func(t *testing.T) {
			sess := controlplane.ConsoleSession{ID: "session", Status: status, CreatedAt: time.Now().Add(-time.Minute)}
			before := sess
			recordConsoleTransportLoss(&sess, "peer disconnected")
			switch status {
			case "queued", "running", "lost":
				require.Equal(t, "lost", sess.Status)
				require.True(t, consoleActivityUnresolved(sess))
			default:
				require.Equal(t, before, sess)
			}
		})
	}
}

func TestConsoleCompletionCanFollowCancellationRequest(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	hub := newConsoleSessionHub(store)
	sess := createConsoleForTest(t, hub)
	require.NoError(t, hub.Cancel(sess.ID, "requested", "user"))
	svc := &AgentService{consoles: hub}
	command := controlplane.AgentCommand{Type: "session_open", Session: &sess}
	svc.updateConsoleSessionFromCommandEvent(command, controlplane.AgentCommandEvent{Status: "started", CreatedAt: time.Now().UTC()})
	current, err := hub.Snapshot(sess.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.ConsoleSessionStatusCancelling, current.Status)
	svc.updateConsoleSessionFromCommandEvent(command, controlplane.AgentCommandEvent{Status: "cancelled", CreatedAt: time.Now().UTC()})
	current, err = hub.Snapshot(sess.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.ConsoleSessionStatusCancelled, current.Status)
	require.False(t, current.EndedAt.IsZero())
}

func TestConsoleCancelReportsDispatchFailureAndRetainsIntent(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	hub := newConsoleSessionHub(store)
	sess := createConsoleForTest(t, hub)
	failure := errors.New("dispatch unavailable")
	service := &ConsoleService{consoles: hub, publishCommand: func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		return controlplane.AgentCommand{}, failure
	}}
	result, err := service.Cancel(context.Background(), sess.ID, requestSubject{User: "operator"})
	require.ErrorIs(t, err, failure)
	require.Equal(t, controlplane.ConsoleSessionStatusCancelling, result.Status)
	stored, err := store.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, result.Status, stored.Status)
	service.publishCommand = func(_ context.Context, agent string, command controlplane.AgentCommand) (controlplane.AgentCommand, error) {
		require.Equal(t, sess.AgentID, agent)
		require.Equal(t, "cancel_command", command.Type)
		require.Equal(t, sess.ID, command.Session.ID)
		return command, nil
	}
	result, err = service.Cancel(context.Background(), sess.ID, requestSubject{User: "operator"})
	require.NoError(t, err)
	require.Equal(t, controlplane.ConsoleSessionStatusCancelling, result.Status)
	require.Len(t, result.Audit, len(stored.Audit))
}
