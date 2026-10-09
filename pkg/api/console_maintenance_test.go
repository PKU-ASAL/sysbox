package api

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestConsoleWritesRespectMaintenanceAndAllowDrain(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		sess := controlplane.ConsoleSession{ID: "session", AgentID: "agent", Topology: "lab", Node: "node", Status: "queued", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveConsoleSession(ctx, sess))
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		barrier, err := authority.BeginMaintenance(ctx, "freeze", uid, "plan")
		require.NoError(t, err)
		fresh := sess
		fresh.ID = "fresh"
		require.ErrorIs(t, store.SaveConsoleSession(ctx, fresh), persistence.ErrConflict)
		items, err := store.ListConsoleSessions(ctx, "")
		require.NoError(t, err)
		require.Len(t, items, 1, "rejected creation cannot publish a session")
		require.NoError(t, store.SaveConsoleSession(ctx, sess), "exact retry does not admit new work")
		running := sess
		running.Status = "running"
		require.ErrorIs(t, store.CompareAndSwapConsoleSession(ctx, sess, running), persistence.ErrConflict)
		cancelling := sess
		cancelling.Status = "cancelling"
		require.NoError(t, store.CompareAndSwapConsoleSession(ctx, sess, cancelling))
		closed := cancelling
		closed.Status, closed.EndedAt = "closed", time.Now().UTC()
		require.NoError(t, store.CompareAndSwapConsoleSession(ctx, cancelling, closed))
		require.ErrorIs(t, store.CompareAndSwapConsoleSession(ctx, closed, running), persistence.ErrConflict)
		lost := closed
		lost.Status = "lost"
		require.ErrorIs(t, store.CompareAndSwapConsoleSession(ctx, closed, lost), persistence.ErrConflict, "resolved evidence cannot become unknown during freeze")
		got, err := store.GetConsoleSession(ctx, sess.ID)
		require.NoError(t, err)
		require.Equal(t, "closed", got.Status)
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		require.NoError(t, store.SaveConsoleSession(ctx, fresh))
	})
}

func TestConsoleAdmissionAndMaintenanceSerializePublication(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		for attempt := 0; attempt < 8; attempt++ {
			id := fmt.Sprintf("session-%d", attempt)
			sess := controlplane.ConsoleSession{ID: id, AgentID: "agent", Topology: "lab", Status: "queued", CreatedAt: time.Now().UTC()}
			start := make(chan struct{})
			var workers sync.WaitGroup
			var admitted, freezeErr, readErr error
			var barrier *persistence.MaintenanceBarrier
			workers.Add(2)
			go func() { defer workers.Done(); <-start; admitted = store.SaveConsoleSession(ctx, sess) }()
			go func() {
				defer workers.Done()
				<-start
				barrier, freezeErr = authority.BeginMaintenance(ctx, id, uid, "plan")
				if freezeErr == nil {
					_, readErr = store.GetConsoleSession(ctx, id)
				}
			}()
			close(start)
			workers.Wait()
			require.NoError(t, freezeErr)
			if admitted == nil {
				require.NoError(t, readErr, "successful admission must be visible when freeze returns")
			} else {
				require.ErrorIs(t, admitted, persistence.ErrConflict)
				require.Error(t, readErr)
			}
			require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		}
	})
}

func TestConsoleMaintenanceUsesLifecycleIdentityAndPreservesLostEvidence(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		grantTestTopologyUID(t, s, "other")
		lost := controlplane.ConsoleSession{ID: "lost", Topology: "lab", Status: "lost", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveConsoleSession(ctx, lost))
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		barrier, err := authority.BeginMaintenance(ctx, "freeze", uid, "plan")
		require.NoError(t, err)
		other := lost
		other.ID, other.Topology, other.Status = "other", "other", "queued"
		require.NoError(t, store.SaveConsoleSession(ctx, other), "a different established UID is not frozen")
		unknown := other
		unknown.ID, unknown.Topology = "unknown", "uninitialized"
		require.ErrorIs(t, store.SaveConsoleSession(ctx, unknown), persistence.ErrConflict)
		updated := lost
		updated.Err = "connection still unavailable"
		updated.EndedAt = time.Now().UTC()
		require.NoError(t, store.CompareAndSwapConsoleSession(ctx, lost, updated), "unknown remote work must retain new observations during freeze")
		require.True(t, persistence.ConsoleSessionUnresolved(updated))
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		require.NoError(t, store.SaveConsoleSession(ctx, unknown))
	})
}

func TestConsoleAdmissionWaitsForUncommittedFirstBarrier(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		locked, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		done := make(chan error, 1)
		go func() {
			done <- (apiExecutionGrantStorage{store: store}).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
				memory := &grantTransaction{raw: raw}
				_, err := persistence.NewExecutionGrantRepository(memory).BeginMaintenance(ctx, "freeze", uid, "plan")
				close(locked)
				<-release
				return memory.raw, err
			})
		}()
		select {
		case <-locked:
		case err := <-done:
			t.Fatalf("could not lock authority: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("authority lock was not acquired")
		}
		deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		sess := controlplane.ConsoleSession{ID: "session", AgentID: "agent", Topology: "lab", Status: "queued", CreatedAt: time.Now().UTC()}
		err := store.SaveConsoleSession(deadline, sess)
		unblock()
		require.NoError(t, <-done)
		require.Error(t, err, "admission must wait for the first barrier transaction")
		items, err := store.ListConsoleSessions(ctx, "")
		require.NoError(t, err)
		require.Empty(t, items, "timed-out admission cannot publish partial state")
		require.ErrorIs(t, store.SaveConsoleSession(ctx, sess), persistence.ErrConflict)
	})
}

func TestConsoleAdmissionRejectsInvalidWorkspaceAcrossStores(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		sess := controlplane.ConsoleSession{ID: "session", Topology: "lab", Workspace: "../outside", Status: "queued", CreatedAt: time.Now().UTC()}
		require.ErrorIs(t, store.SaveConsoleSession(context.Background(), sess), persistence.ErrInvalidRecord)
		items, err := store.ListConsoleSessions(context.Background(), "")
		require.NoError(t, err)
		require.Empty(t, items)
	})
}
