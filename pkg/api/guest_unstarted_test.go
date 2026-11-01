package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestCancelledUnstartedGuestIsClosedAfterDispatchRetirement(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "execution", CommandID: "source", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "queued", CreatedAt: time.Now().UTC()}
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ActionID: x.ID, TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
		require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
		_, err := store.CancelGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.NoError(t, (&AgentService{store: store}).reconcileGuestExecutionCancellations(ctx))
		got, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, "cancelled", got.Status)
		require.Equal(t, "not_started", got.ResultClass)
		require.True(t, got.CancelRequested)
		require.False(t, got.EndedAt.IsZero())
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
		require.NoError(t, (&AgentService{store: store}).reconcileGuestExecutionCancellations(ctx))
		repeated, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, got, repeated)
	})
}

type interruptedGuestRetirement struct {
	controlstore.Store
	failID    string
	failFinal bool
}

func (s interruptedGuestRetirement) CancelGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	if s.failFinal {
		x, err := s.Store.GetGuestExecution(ctx, id)
		if err != nil {
			return nil, err
		}
		cmd, err := readFailureCommand(ctx, s.Store, x.CommandID)
		if err != nil {
			return nil, err
		}
		if cmd != nil && cmd.Retired {
			return nil, errors.New("injected retirement failure after source tombstone")
		}
	}
	return s.Store.CancelGuestExecution(ctx, id)
}

func (s interruptedGuestRetirement) DeleteAgentCommand(ctx context.Context, agent, id string) error {
	if id == s.failID {
		return errors.New("injected retirement failure")
	}
	return s.Store.DeleteAgentCommand(ctx, agent, id)
}

func TestUnstartedCancellationRecoversInterruptedRetirement(t *testing.T) {
	for _, stage := range []string{"source", "completion", "cancellation"} {
		t.Run(stage, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
				ctx := context.Background()
				x := controlplane.GuestExecution{ID: "execution", CommandID: "source", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "queued", CreatedAt: time.Now().UTC()}
				cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ActionID: x.ID, TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
				require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
				// A worker start report is not evidence of remote execution authority.
				started, err := newGuestExecutionService(store, nil, nil).MarkRunning(ctx, x.ID)
				require.NoError(t, err)
				require.False(t, started.StartedAt.IsZero())
				_, err = store.CancelGuestExecution(ctx, x.ID)
				require.NoError(t, err)
				failID := x.CommandID
				if stage == "completion" {
					failID = ""
				}
				if stage == "cancellation" {
					cancelCmd, err := persistence.GuestExecutionCancellationCommand(x)
					require.NoError(t, err)
					failID = cancelCmd.ID
				}
				interrupted := &AgentService{store: interruptedGuestRetirement{Store: store, failID: failID, failFinal: stage == "completion"}}
				require.ErrorContains(t, interrupted.reconcileGuestExecutionCancellations(ctx), "injected retirement failure")
				retained, err := store.GetGuestExecution(ctx, x.ID)
				require.NoError(t, err)
				require.True(t, retained.CancelRequested)
				if stage != "cancellation" {
					require.Equal(t, "running", retained.Status)
				} else {
					require.Equal(t, "cancelled", retained.Status)
				}
				require.NoError(t, (&AgentService{store: store}).reconcileGuestExecutionCancellations(ctx))
				finished, err := store.GetGuestExecution(ctx, x.ID)
				require.NoError(t, err)
				require.Equal(t, "cancelled", finished.Status)
				require.Equal(t, "not_started", finished.ResultClass)
				commands, err := store.ListAgentCommands(ctx, "agent")
				require.NoError(t, err)
				require.Empty(t, commands)
			})
		})
	}
}

func TestCancelledGuestWithHistoricalGrantCannotClaimNeverStarted(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "execution", CommandID: "source", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "queued", CreatedAt: time.Now().UTC()}
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ActionID: x.ID, TopologyUID: "topology", ResourceUID: "resource", Generation: 1}
		require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "agent", "host", "")
		require.NoError(t, err)
		request := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: x.CommandID, ActionID: x.ID, Attempt: 1, Operation: "guest_task", Digest: "digest"}
		old, err := authority.Issue(ctx, request, time.Minute)
		require.NoError(t, err)
		require.NoError(t, authority.AcceptHostStop(ctx, *old, "stop"))
		request.CommandID, request.ActionID = "successor", "successor"
		_, err = authority.Issue(ctx, request, time.Minute)
		require.NoError(t, err)
		_, err = store.CancelGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.NoError(t, (&AgentService{store: store}).reconcileGuestExecutionCancellations(ctx))
		got, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, "queued", got.Status)
		require.True(t, got.EndedAt.IsZero())
	})
}
