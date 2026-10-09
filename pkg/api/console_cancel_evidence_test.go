package api

import (
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
	require.True(t, persistence.ConsoleSessionUnresolved(*current))
	require.NoError(t, hub.Cancel(sess.ID, "retry", "user"))
	again, err := store.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, len(current.Audit), len(again.Audit), "retry must not append another cancellation event")
	_ = newConsoleSessionHub(store)
	recovered, err := store.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.True(t, persistence.ConsoleSessionUnresolved(*recovered), "restart cannot turn cancel request into completion")
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

func (s *delayedConsoleSave) CompareAndSwapConsoleSession(ctx context.Context, old, next controlplane.ConsoleSession) error {
	if next.Status == controlplane.ConsoleSessionStatusRunning {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	return s.apiStore.CompareAndSwapConsoleSession(ctx, old, next)
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

func (s failingConsoleSave) CreateConsoleDispatch(context.Context, controlplane.ConsoleSession, controlplane.AgentCommand) error {
	return errors.New("storage unavailable")
}

func (s failingConsoleSave) SaveConsoleSession(context.Context, controlplane.ConsoleSession) error {
	return errors.New("storage unavailable")
}

func (s failingConsoleSave) CompareAndSwapConsoleSession(context.Context, controlplane.ConsoleSession, controlplane.ConsoleSession) error {
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
				require.True(t, persistence.ConsoleSessionUnresolved(sess))
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

func TestConsoleCancellationRetriesReuseDurableCommand(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		hub := newConsoleSessionHub(store)
		sess, err := hub.Create(ctx, "lab", "node", "agent", controlplane.ConsoleRequest{})
		require.NoError(t, err)
		agents := &AgentService{store: store}
		service := &ConsoleService{consoles: hub, publishCommand: agents.PublishCommand}
		_, err = service.Cancel(ctx, sess.ID, requestSubject{User: "operator"})
		require.NoError(t, err)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", commands[0].ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = service.Cancel(ctx, sess.ID, requestSubject{User: "operator"})
		require.NoError(t, err)
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1, "retry must not manufacture another command")
		require.Equal(t, leased.ID, commands[0].ID)
		require.Equal(t, leased.Attempt, commands[0].Attempt)
		require.Equal(t, leased.LeaseOwner, commands[0].LeaseOwner)
		service.consoles = newConsoleSessionHub(store)
		_, err = service.Cancel(ctx, sess.ID, requestSubject{User: "operator"})
		require.NoError(t, err)
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, leased.ID, commands[0].ID, "cold retry has the same immutable cancellation identity")
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", leased.ID))
		_, err = service.Cancel(ctx, sess.ID, requestSubject{User: "operator"})
		require.Error(t, err, "retirement cannot resurrect the cancellation delivery")
		commands, err = store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
	})
}

func TestConsoleCancellationConcurrentFirstEnqueueIsIdempotent(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		hub := newConsoleSessionHub(store)
		sess, err := hub.Create(ctx, "lab", "node", "agent", controlplane.ConsoleRequest{})
		require.NoError(t, err)
		cmd := consoleCancellationCommand(sess)
		publisher := &AgentService{store: store}
		start := make(chan struct{})
		results := make(chan error, 8)
		for i := 0; i < 8; i++ {
			go func() { <-start; _, err := publisher.PublishCommand(ctx, "agent", cmd); results <- err }()
		}
		close(start)
		for i := 0; i < 8; i++ {
			require.NoError(t, <-results)
		}
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 1)
		require.Equal(t, cmd.ID, commands[0].ID)
	})
}
