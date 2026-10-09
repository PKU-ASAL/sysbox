package api

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGuestDispatchCannotEnterFrozenTopology(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		barrier, err := authority.BeginMaintenance(ctx, "restore", "owner", "plan")
		require.NoError(t, err)
		for _, kind := range []string{"execution", "file", "node"} {
			t.Run(kind, func(t *testing.T) {
				cmd := controlplane.AgentCommand{ID: "cmd-" + kind, AgentID: "agent", TopologyUID: "owner", Status: controlplane.AgentCommandStatusQueued}
				create := func() error {
					if kind == "node" {
						op, dispatch := nodeDispatchFixture()
						dispatch.TopologyUID = cmd.TopologyUID
						return store.CreateNodeOperationDispatch(ctx, op, dispatch)
					}
					if kind == "execution" {
						x := controlplane.GuestExecution{ID: kind, CommandID: cmd.ID, AgentID: cmd.AgentID, Topology: "lab", Node: "node", Version: 1, Status: controlplane.GuestExecutionQueued, Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
						cmd.Type, cmd.Execution, cmd.ExecutionRequest = "guest_execution", &x, x.Request
						return store.CreateGuestExecutionDispatch(ctx, x, cmd)
					}
					x := controlplane.GuestFileOperation{ID: kind, CommandID: cmd.ID, AgentID: cmd.AgentID, Topology: "lab", Node: "node", Version: 1, Status: controlplane.GuestExecutionQueued}
					cmd.Type, cmd.FilePut = "guest_file_put", &controlplane.GuestFilePut{ID: x.ID, Topology: x.Topology, Node: x.Node}
					return store.CreateGuestFileDispatch(ctx, x, cmd)
				}
				require.ErrorIs(t, create(), persistence.ErrConflict)
				if kind == "execution" {
					_, err = store.GetGuestExecution(ctx, kind)
				} else if kind == "node" {
					_, err = store.GetNodeOperation(ctx, "op")
				} else {
					_, err = store.GetGuestFileOperation(ctx, kind)
				}
				require.ErrorIs(t, err, persistence.ErrNotFound, "frozen rejection must publish neither operation nor command")
				commands, err := store.ListAgentCommands(ctx, "agent")
				require.NoError(t, err)
				require.Empty(t, commands)
				cmd.TopologyUID = ""
				require.ErrorIs(t, create(), persistence.ErrInvalidRecord, "missing UID cannot bypass a freeze")
			})
		}
		require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		cmd := controlplane.AgentCommand{ID: "resumed", AgentID: "agent", TopologyUID: "owner", Type: "guest_execution", Status: controlplane.AgentCommandStatusQueued}
		x := controlplane.GuestExecution{ID: "resumed", CommandID: cmd.ID, AgentID: cmd.AgentID, Topology: "lab", Node: "node", Version: 1, Status: controlplane.GuestExecutionQueued, Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
		cmd.Execution, cmd.ExecutionRequest = &x, x.Request
		require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
	})
}

func TestGuestAdmissionAndMaintenanceSerializePublication(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		for attempt := 0; attempt < 8; attempt++ {
			id := fmt.Sprintf("request-%d", attempt)
			x := controlplane.GuestExecution{ID: id, CommandID: "cmd-" + id, AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: controlplane.GuestExecutionQueued, Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
			command := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, TopologyUID: "owner", Type: "guest_execution", Status: controlplane.AgentCommandStatusQueued, Execution: &x, ExecutionRequest: x.Request}
			start := make(chan struct{})
			var workers sync.WaitGroup
			var admitted, freezeErr, readErr error
			var barrier *persistence.MaintenanceBarrier
			var visibleAtFreeze bool
			workers.Add(2)
			go func() { defer workers.Done(); <-start; admitted = store.CreateGuestExecutionDispatch(ctx, x, command) }()
			go func() {
				defer workers.Done()
				<-start
				barrier, freezeErr = authority.BeginMaintenance(ctx, id, "owner", "plan")
				if freezeErr != nil {
					return
				}
				_, readErr = store.GetGuestExecution(ctx, id)
				visibleAtFreeze = readErr == nil
			}()
			close(start)
			workers.Wait()
			require.NoError(t, freezeErr)
			if admitted == nil {
				require.True(t, visibleAtFreeze, "successful admission must publish before freeze returns")
			} else {
				require.ErrorIs(t, admitted, persistence.ErrConflict)
				require.ErrorIs(t, readErr, persistence.ErrNotFound)
			}
			require.NoError(t, authority.EndMaintenance(ctx, *barrier))
		}
	})
}
