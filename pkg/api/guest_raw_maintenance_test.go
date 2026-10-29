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

func TestRawGuestRecordsRespectMaintenanceAndDrain(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		x := controlplane.GuestExecution{ID: "exec", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", Status: "queued", CreatedAt: time.Now().UTC()}
		f := controlplane.GuestFileOperation{ID: "file", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", Status: "queued", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveGuestExecution(ctx, x))
		require.NoError(t, store.SaveGuestFileOperation(ctx, f))
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		barrier, err := authority.BeginMaintenance(ctx, "freeze", uid, "plan")
		require.NoError(t, err)
		freshX, freshF := x, f
		freshX.ID, freshF.ID = "new-exec", "new-file"
		require.ErrorIs(t, store.SaveGuestExecution(ctx, freshX), persistence.ErrConflict)
		require.ErrorIs(t, store.SaveGuestFileOperation(ctx, freshF), persistence.ErrConflict)
		freshF.CancelRequested = true
		require.ErrorIs(t, store.SaveGuestFileOperation(ctx, freshF), persistence.ErrConflict)
		runningX, runningF := x, f
		runningX.Status, runningF.Status = "running", "running"
		ok, err := store.CompareAndSwapGuestExecution(ctx, runningX, 1)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.False(t, ok)
		ok, err = store.CompareAndSwapGuestFileOperation(ctx, runningF, 1)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.False(t, ok)
		runningF.CancelRequested = true
		ok, err = store.CompareAndSwapGuestFileOperation(ctx, runningF, 1)
		require.ErrorIs(t, err, persistence.ErrConflict, "cancel intent cannot smuggle an active-state transition")
		require.False(t, ok)
		f.CancelRequested = true
		ok, err = store.CompareAndSwapGuestFileOperation(ctx, f, 1)
		require.NoError(t, err)
		require.True(t, ok, "cancellation intent must drain")
		x.Status, x.EndedAt = "failed", time.Now().UTC()
		ok, err = store.CompareAndSwapGuestExecution(ctx, x, 1)
		require.NoError(t, err)
		require.True(t, ok)
		f.Version, f.Status, f.EndedAt = 2, "cancelled", time.Now().UTC()
		ok, err = store.CompareAndSwapGuestFileOperation(ctx, f, 2)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		require.NoError(t, store.SaveGuestExecution(ctx, freshX))
		freshF.CancelRequested = false
		require.NoError(t, store.SaveGuestFileOperation(ctx, freshF))
	})
}

func TestRawGuestPublicationWaitsForAuthorityTransaction(t *testing.T) {
	for _, kind := range []string{"execution", "file"} {
		t.Run(kind, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
				ctx := context.Background()
				var authority persistence.ExecutionGrantStorage = controlstore.AuthorityStorage{Store: store}
				if local, ok := store.(*controlstore.Local); ok {
					authority = persistence.FilesystemExecutionGrantStorage(local.RunsDir)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				done := make(chan error, 1)
				go func() {
					done <- authority.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
						memory := &controlstore.GrantTransaction{Raw: raw}
						err := persistence.NewExecutionGrantRepository(memory).ObserveTime(ctx)
						close(entered)
						<-release
						return memory.Raw, err
					})
				}()
				select {
				case <-entered:
				case err := <-done:
					t.Fatalf("authority transaction failed: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("authority exclusion was not acquired")
				}
				x := controlplane.GuestExecution{ID: "operation", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", Status: "queued", CreatedAt: time.Now().UTC()}
				f := controlplane.GuestFileOperation{ID: "operation", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", Status: "queued", CreatedAt: time.Now().UTC()}
				write := func(ctx context.Context) error {
					if kind == "execution" {
						return store.SaveGuestExecution(ctx, x)
					}
					return store.SaveGuestFileOperation(ctx, f)
				}
				deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
				defer cancel()
				err := write(deadline)
				unblock()
				require.NoError(t, <-done)
				require.Error(t, err)
				if kind == "execution" {
					_, err = store.GetGuestExecution(ctx, x.ID)
				} else {
					_, err = store.GetGuestFileOperation(ctx, f.ID)
				}
				require.ErrorIs(t, err, persistence.ErrNotFound)
				require.NoError(t, write(ctx))
			})
		})
	}
}

func TestMetadataDeleteRechecksGuestRecordsInsideCommit(t *testing.T) {
	for _, kind := range []string{"execution", "file"} {
		t.Run(kind, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
				ctx := context.Background()
				s := stateDeleteTestServer(t, store)
				repo, err := s.actionRepository("lab")
				require.NoError(t, err)
				before, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				require.NoError(t, s.workspaceService().EnsureTopologyQuiescent(ctx, "lab"))
				x := controlplane.GuestExecution{ID: "late", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", Status: "queued", CreatedAt: time.Now().UTC()}
				f := controlplane.GuestFileOperation{ID: "late", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", Status: "queued", CreatedAt: time.Now().UTC()}
				if kind == "execution" {
					require.NoError(t, store.SaveGuestExecution(ctx, x))
				} else {
					require.NoError(t, store.SaveGuestFileOperation(ctx, f))
				}
				require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), persistence.ErrConflict)
				var ok bool
				if kind == "execution" {
					x.Status, x.EndedAt = "failed", time.Now().UTC()
					ok, err = store.CompareAndSwapGuestExecution(ctx, x, 1)
				} else {
					f.Status, f.EndedAt = "failed", time.Now().UTC()
					ok, err = store.CompareAndSwapGuestFileOperation(ctx, f, 1)
				}
				require.NoError(t, err)
				require.True(t, ok)
				require.NoError(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID))
			})
		})
	}
}

func TestMetadataDeleteRejectsIncompleteGuestTerminalEvidence(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		before, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		now := time.Now().UTC()
		x := controlplane.GuestExecution{ID: "incomplete", Version: 1, Topology: "lab", Status: "completed", CreatedAt: now}
		require.NoError(t, store.SaveGuestExecution(ctx, x))
		f := controlplane.GuestFileOperation{ID: "reversed", Version: 1, Topology: "lab", Status: "completed", CreatedAt: now, EndedAt: now.Add(-time.Second)}
		require.NoError(t, store.SaveGuestFileOperation(ctx, f))
		activities, err := store.ListGuestActivity(ctx, "lab")
		require.NoError(t, err)
		require.Len(t, activities, 2)
		for _, activity := range activities {
			require.True(t, activity.Unresolved())
		}
		require.ErrorIs(t, s.workspaceService().EnsureTopologyQuiescent(ctx, "lab"), persistence.ErrConflict)
		require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), persistence.ErrConflict)
		after, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, before.TopologyUID, after.TopologyUID)
		require.Equal(t, before.Meta.Serial, after.Meta.Serial)
	})
}
