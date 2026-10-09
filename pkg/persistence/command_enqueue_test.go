package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestCommandEnqueuePreservesDeliveryAndRejectsChangedPayload(t *testing.T) {
	ctx := context.Background()
	storage := &commandStorageFixture{}
	repo := NewCommandRepository(storage)
	cmd := controlplane.AgentCommand{ID: "cancel", AgentID: "agent", Type: "cancel_command", Session: &controlplane.ConsoleSession{ID: "session"}}
	_, err := repo.Enqueue(ctx, cmd)
	require.NoError(t, err)
	lease, ok, err := repo.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	actual, err := repo.Enqueue(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, lease, actual)
	changed := cmd
	changed.Session = &controlplane.ConsoleSession{ID: "different"}
	_, err = repo.Enqueue(ctx, changed)
	require.ErrorIs(t, err, ErrConflict)
	storage.commitError = errors.New("commit failed")
	actual, err = repo.Enqueue(ctx, cmd)
	require.Error(t, err)
	require.Nil(t, actual, "no speculative command after storage failure")
}
