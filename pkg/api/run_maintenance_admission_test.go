package api

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestRunDispatchMaintenanceBlocksNewWorkButPreservesReplay(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		request := func(id string) controlstore.RunDispatchRequest {
			run := controlplane.Run{ID: id, Topology: "lab", AgentID: "agent", Op: "apply", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
			r := incarnationDispatch(run)
			r.Command.TopologyUID = "owner"
			return r
		}
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		queued := controlplane.Run{ID: "queued", Topology: "lab", Status: controlplane.RunQueued, Op: "apply", QueuedAt: time.Now().UTC()}
		require.NoError(t, store.SaveRun(ctx, queued))
		barrier, err := authority.BeginMaintenance(ctx, "freeze", "owner", "plan")
		require.NoError(t, err)
		first := request("first")
		run, created, err := store.CreateRunDispatch(ctx, first)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.Nil(t, run)
		require.False(t, created)
		_, err = store.GetRun(ctx, first.Run.ID)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		_, found, err := store.GetRunDispatch(ctx, controlstore.RunDispatchKey(first), first.Fingerprint)
		require.NoError(t, err)
		require.False(t, found, "rejection must roll back idempotency mapping")
		assigned := queued
		assigned.MarkAssigned("agent", time.Now().UTC())
		dispatch := incarnationDispatch(assigned)
		dispatch.Command.TopologyUID = "owner"
		_, _, err = store.CreateRunDispatch(ctx, dispatch)
		require.ErrorIs(t, err, persistence.ErrConflict)
		current, err := store.GetRun(ctx, queued.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunQueued, current.Status)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		_, _, err = store.CreateRunDispatch(ctx, dispatch)
		require.NoError(t, err)
		_, err = authority.BeginMaintenance(ctx, "again", "owner", "next-plan")
		require.NoError(t, err)
		replay, created, err := store.CreateRunDispatch(ctx, dispatch)
		require.NoError(t, err, "exact committed retry remains a historical read while frozen")
		require.False(t, created)
		require.Equal(t, controlplane.RunAssigned, replay.Status)
	})
}

func TestRunDispatchAndMaintenanceSerializePublication(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		for i := 0; i < 6; i++ {
			id := fmt.Sprintf("run-%d", i)
			run := controlplane.Run{ID: id, Topology: "lab-" + id, AgentID: "agent", Op: "apply", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
			request := incarnationDispatch(run)
			request.Command.TopologyUID = "owner"
			start := make(chan struct{})
			var workers sync.WaitGroup
			workers.Add(2)
			var admitted, freezeErr, readErr error
			var barrier *persistence.MaintenanceBarrier
			var result *controlplane.Run
			var visible bool
			go func() { defer workers.Done(); <-start; result, _, admitted = store.CreateRunDispatch(ctx, request) }()
			go func() {
				defer workers.Done()
				<-start
				barrier, freezeErr = authority.BeginMaintenance(ctx, id, "owner", "plan")
				if freezeErr == nil {
					_, readErr = store.GetRun(ctx, id)
					visible = readErr == nil
				}
			}()
			close(start)
			workers.Wait()
			require.NoError(t, freezeErr)
			if admitted == nil {
				require.True(t, visible, "accepted Run must publish before maintenance returns")
				require.NotNil(t, result)
			} else {
				require.ErrorIs(t, admitted, persistence.ErrConflict)
				require.ErrorIs(t, readErr, persistence.ErrNotFound)
				require.Nil(t, result)
			}
			require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		}
	})
}
