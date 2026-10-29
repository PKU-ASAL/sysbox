package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestConsoleCASHasOneWinnerAndRejectsBlindOverwrite(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		original := controlplane.ConsoleSession{ID: "session", AgentID: "agent", Topology: "lab", Workspace: "workspace", Status: "queued", CreatedAt: time.Now().UTC()}
		require.ErrorIs(t, store.CompareAndSwapConsoleSession(ctx, original, original), persistence.ErrConflict)
		require.NoError(t, store.SaveConsoleSession(ctx, original))
		require.NoError(t, store.SaveConsoleSession(ctx, original), "an exact create retry is idempotent")
		a, b := original, original
		a.Status, b.Status = "running", "cancelling"
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, next := range []controlplane.ConsoleSession{a, b} {
			wg.Add(1)
			go func(next controlplane.ConsoleSession) {
				defer wg.Done()
				<-start
				results <- store.CompareAndSwapConsoleSession(ctx, original, next)
			}(next)
		}
		close(start)
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else {
				require.ErrorIs(t, err, persistence.ErrConflict)
			}
		}
		require.Equal(t, 1, wins)
		require.ErrorIs(t, store.SaveConsoleSession(ctx, original), persistence.ErrConflict)
		current, err := store.GetConsoleSession(ctx, original.ID)
		require.NoError(t, err)
		for name, mutate := range map[string]func(*controlplane.ConsoleSession){
			"id":                func(s *controlplane.ConsoleSession) { s.ID = "other" },
			"agent":             func(s *controlplane.ConsoleSession) { s.AgentID = "other" },
			"topology":          func(s *controlplane.ConsoleSession) { s.Topology = "other" },
			"node":              func(s *controlplane.ConsoleSession) { s.Node = "other" },
			"project":           func(s *controlplane.ConsoleSession) { s.ProjectID = "other" },
			"workspace":         func(s *controlplane.ConsoleSession) { s.Workspace = "other" },
			"created_at":        func(s *controlplane.ConsoleSession) { s.CreatedAt = s.CreatedAt.Add(time.Second) },
			"created_at_offset": func(s *controlplane.ConsoleSession) { s.CreatedAt = s.CreatedAt.In(time.FixedZone("other", 8*60*60)) },
		} {
			foreign := *current
			mutate(&foreign)
			require.ErrorIs(t, store.CompareAndSwapConsoleSession(ctx, *current, foreign), persistence.ErrConflict, name)
		}
	})
}

func TestConsoleCASCancelConflictCanRetryWithFreshObservation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		sess := controlplane.ConsoleSession{ID: "session", AgentID: "agent", Topology: "lab", Node: "node", Status: "lost", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveConsoleSession(context.Background(), sess))
		first, second := newConsoleSessionHub(store), newConsoleSessionHub(store)
		require.NoError(t, first.Update(sess.ID, func(s *controlplane.ConsoleSession) { s.Err = "new observation" }))
		require.ErrorIs(t, second.Cancel(context.Background(), sess.ID, "stop", requestSubject{User: "user"}), persistence.ErrConflict)
		st, err := second.Get(sess.ID)
		require.NoError(t, err)
		select {
		case <-st.cancel:
			t.Fatal("uncommitted cancel must not signal the relay")
		default:
		}
		require.NoError(t, second.Cancel(context.Background(), sess.ID, "stop", requestSubject{User: "user"}))
		stored, err := store.GetConsoleSession(context.Background(), sess.ID)
		require.NoError(t, err)
		require.Equal(t, "cancelling", stored.Status)
		require.Len(t, stored.Audit, 1)
	})
}

func TestConsoleCASConflictRefreshesIndependentHubWithoutReplayingUpdate(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		sess := controlplane.ConsoleSession{ID: "session", AgentID: "agent", Topology: "lab", Node: "node", Status: "lost", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveConsoleSession(ctx, sess))
		first, second := newConsoleSessionHub(store), newConsoleSessionHub(store)
		require.NoError(t, first.Cancel(context.Background(), sess.ID, "stop", requestSubject{User: "user"}))
		calls := 0
		err := second.Update(sess.ID, func(s *controlplane.ConsoleSession) {
			calls++
			s.Status = "running"
		})
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.Equal(t, 1, calls, "a stale callback must never be replayed automatically")
		observed, err := second.Snapshot(sess.ID)
		require.NoError(t, err)
		require.Equal(t, "cancelling", observed.Status)
		require.NoError(t, second.Cancel(context.Background(), sess.ID, "stop again", requestSubject{User: "user"}))
		stored, err := store.GetConsoleSession(ctx, sess.ID)
		require.NoError(t, err)
		require.Len(t, stored.Audit, 1, "retry must not duplicate cancellation audit")
	})
}

type staleConsoleInventory struct {
	controlstore.Store
	session controlplane.ConsoleSession
}

func (s staleConsoleInventory) ListConsoleSessions(context.Context, string) ([]controlplane.ConsoleSession, error) {
	return []controlplane.ConsoleSession{s.session}, nil
}

func TestConsoleCASConcurrentStartupAdoptsPersistedObservation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		sess := controlplane.ConsoleSession{ID: "session", AgentID: "agent", Topology: "lab", Status: "running", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveConsoleSession(context.Background(), sess))
		first := newConsoleSessionHub(store)
		require.NoError(t, first.initErr)
		second := newConsoleSessionHub(staleConsoleInventory{Store: store, session: sess})
		require.NoError(t, second.initErr)
		a, err := first.Snapshot(sess.ID)
		require.NoError(t, err)
		b, err := second.Snapshot(sess.ID)
		require.NoError(t, err)
		require.Equal(t, a, b)
	})
}

func TestConsoleCASStartupRechecksActiveObservation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		queued := controlplane.ConsoleSession{ID: "session", AgentID: "agent", Topology: "lab", Status: "queued", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveConsoleSession(ctx, queued))
		running := queued
		running.Status = "running"
		running.StartedAt = time.Now().UTC()
		require.NoError(t, store.CompareAndSwapConsoleSession(ctx, queued, running))
		hub := newConsoleSessionHub(staleConsoleInventory{Store: store, session: queued})
		require.NoError(t, hub.initErr)
		observed, err := hub.Snapshot(queued.ID)
		require.NoError(t, err)
		require.Equal(t, "lost", observed.Status)
		require.Equal(t, running.StartedAt, observed.StartedAt)
	})
}
