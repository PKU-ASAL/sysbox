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

func TestRawNodeOperationCannotEnterMaintenance(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		grantTestTopologyUID(t, s, "other")
		op := controlplane.NodeOperation{ID: "operation", Topology: "lab", Node: "node", AgentID: "agent", Operation: "pause", Status: "queued", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.CreateNodeOperation(ctx, op))
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		barrier, err := authority.BeginMaintenance(ctx, "freeze", uid, "plan")
		require.NoError(t, err)
		fresh := op
		fresh.ID = "fresh"
		require.ErrorIs(t, store.CreateNodeOperation(ctx, fresh), persistence.ErrConflict)
		_, err = store.GetNodeOperation(ctx, fresh.ID)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		other := op
		other.ID, other.Topology = "other", "other"
		require.NoError(t, store.CreateNodeOperation(ctx, other))
		other.ID, other.Topology = "unknown", "uninitialized"
		require.ErrorIs(t, store.CreateNodeOperation(ctx, other), persistence.ErrConflict)
		running := op
		running.Status = "running"
		require.ErrorIs(t, store.CompareAndSwapNodeOperation(ctx, op, running), persistence.ErrConflict)
		done := op
		done.Status, done.EndedAt = "failed", time.Now().UTC()
		require.NoError(t, store.CompareAndSwapNodeOperation(ctx, op, done), "completion must drain during freeze")
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		require.NoError(t, store.CreateNodeOperation(ctx, fresh))
	})
}

func TestRawNodePublicationWaitsForAuthorityTransaction(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		var authority persistence.ExecutionGrantStorage = controlstore.AuthorityStorage{Store: store}
		if local, ok := store.(*controlstore.Local); ok {
			// Hold authority alone, proving raw node writers do not depend on
			// an incidental API record lock to serialize with deletion.
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
		deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		op := controlplane.NodeOperation{ID: "operation", Topology: "lab", AgentID: "agent", Node: "node", Operation: "pause", Status: "queued", CreatedAt: time.Now().UTC()}
		err := store.CreateNodeOperation(deadline, op)
		unblock()
		require.NoError(t, <-done)
		require.Error(t, err)
		_, err = store.GetNodeOperation(ctx, op.ID)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		require.NoError(t, store.CreateNodeOperation(ctx, op))
	})
}

func TestMetadataDeleteRechecksNodeOperationsInsideCommit(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		before, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		require.NoError(t, s.workspaceService().EnsureTopologyQuiescent(ctx, "lab"))
		op := controlplane.NodeOperation{ID: "late", Topology: "lab", Workspace: "other-workspace", AgentID: "agent", Node: "node", Operation: "pause", Status: "queued", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.CreateNodeOperation(ctx, op))
		require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), persistence.ErrConflict)
		done := op
		done.Status, done.EndedAt = "failed", time.Now().UTC()
		require.NoError(t, store.CompareAndSwapNodeOperation(ctx, op, done))
		require.NoError(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID))
	})
}
