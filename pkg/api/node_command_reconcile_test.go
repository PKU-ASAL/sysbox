package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, op.AgentID, "host", "")
		require.NoError(t, err)
		request, err := controlplane.FromNodeOperationCommand(*leased)
		require.NoError(t, err)
		digest, err := request.Digest()
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: op.ResourceUID, Generation: op.Generation}, AgentID: op.AgentID, SessionID: session.ID, CommandID: cmd.ID, ActionID: op.ID, Attempt: uint64(leased.Attempt), Operation: "node_power", Digest: digest, TargetBootID: "host"}, time.Minute)
		require.NoError(t, err)
		_, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		receipt, err := controlplane.SignExecutionStopReceipt(key, *grant, "host", "result", time.Now())
		require.NoError(t, err)
		// Exercise result commit before delivery retirement, as after a lost reply.
		transition := func(current controlplane.NodeOperation, record *persistence.CommandRecord) (controlplane.NodeOperation, error) {
			return persistence.CompletePowerFromHost(current, record, op.AgentID, op.ID, receipt)
		}
		switch adapter := store.(type) {
		case *localAPIStore:
			_, err = adapter.completeNodeOperation(ctx, op.AgentID, op.ID, &receipt, transition)
		case *sqliteAPIStore:
			_, err = adapter.completeNodeOperation(ctx, op.AgentID, op.ID, &receipt, transition)
		case *postgresAPIStore:
			_, err = adapter.completeNodeOperation(ctx, op.AgentID, op.ID, &receipt, transition)
		}
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
