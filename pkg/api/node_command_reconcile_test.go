package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestTerminalNodeOperationSettlesLostDeliveryReceipt(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		op, cmd := nodeDispatchFixture()
		require.NoError(t, store.CreateNodeOperationDispatch(ctx, op, cmd))
		leased, ok, err := store.AcquireAgentCommandLease(ctx, cmd.AgentID, cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		leased.Status = controlplane.AgentCommandStatusDelivered
		require.NoError(t, store.SaveAgentCommand(ctx, *leased))
		op.Status = controlplane.NodeOperationStatusDone
		op.EndedAt = time.Now().UTC()
		op.LeaseOwner, op.Attempt, op.LeaseEpoch = leased.LeaseOwner, leased.Attempt, leased.LeaseEpoch
		_, err = store.CompleteNodeOperation(ctx, op.AgentID, op.ID, op)
		require.NoError(t, err)
		service := AgentService{store: store}
		reconciled, terminal := service.ReconcileCommandState(ctx, *leased)
		require.True(t, terminal)
		require.Equal(t, controlplane.AgentCommandStatusCompleted, reconciled.Status)
		current, err := service.FindCommand(ctx, cmd.AgentID, cmd.ID)
		require.NoError(t, err)
		require.Equal(t, reconciled.Status, current.Status)
	})
}
func TestPowerGrantSourceRejectsTerminalOrChangedOperation(t *testing.T) {
	op, cmd := nodeDispatchFixture()
	for _, scenario := range []string{"live", "done", "failed", "missing", "generation", "action"} {
		t.Run(scenario, func(t *testing.T) {
			current := op
			switch scenario {
			case "done":
				current.Status = controlplane.NodeOperationStatusDone
			case "failed":
				current.Status = controlplane.NodeOperationStatusFailed
			case "generation":
				current.Generation++
			case "action":
				current.ActionID = "other"
			}
			err := (apiExecutionGrantStorage{}).checkGuestSSHSource(&persistence.CommandRecord{Command: cmd}, func(string) (*controlplane.NodeOperation, error) {
				if scenario == "missing" {
					return nil, persistence.ErrNotFound
				}
				return &current, nil
			})
			if scenario == "live" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestUnknownNodeCommandDoesNotBecomeRetryableByAge(t *testing.T) {
	service, store, _, _ := newTestAgentService(t)
	op, cmd := nodeDispatchFixture()
	require.NoError(t, store.CreateNodeOperationDispatch(context.Background(), op, cmd))
	cmd.Status = controlplane.AgentCommandStatusRunning
	cmd.LeaseUntil = time.Now().Add(-time.Hour)
	cmd.CreatedAt = time.Now().Add(-24 * time.Hour)
	result, terminal := service.ReconcileCommandState(context.Background(), cmd)
	require.False(t, terminal)
	require.Equal(t, controlplane.AgentCommandStatusRunning, result.Status)
	require.False(t, result.Leasable(time.Now()))
}
