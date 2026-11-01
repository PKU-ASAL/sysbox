package persistence

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObservationSessionAndSequenceRejectDelayedPublication(t *testing.T) {
	ctx := context.Background()
	repo := NewFilesystemExecutionGrantRepository(t.TempDir())
	first, err := repo.OpenSession(ctx, "agent", "process-one", "")
	require.NoError(t, err)
	require.NoError(t, repo.PublishObservation(ctx, "agent", first.ID, "inventory", 2, json.RawMessage(`{"value":"new"}`)))
	require.ErrorIs(t, repo.PublishObservation(ctx, "agent", first.ID, "inventory", 1, json.RawMessage(`{"value":"old"}`)), ErrConflict)
	second, err := repo.OpenSession(ctx, "agent", "process-two", first.ID)
	require.NoError(t, err)
	require.ErrorIs(t, repo.PublishObservation(ctx, "agent", first.ID, "inventory", 3, json.RawMessage(`{"value":"delayed"}`)), ErrConflict)
	require.NoError(t, repo.PublishObservation(ctx, "agent", second.ID, "inventory", 1, json.RawMessage(`{"value":"restarted"}`)))
	record, err := repo.ReadObservation(ctx, "agent", "inventory")
	require.NoError(t, err)
	require.Equal(t, second.ID, record.SessionID)
	require.JSONEq(t, `{"value":"restarted"}`, string(record.Data))
	require.ErrorIs(t, repo.PublishObservation(ctx, "agent", second.ID, "inventory", 1, json.RawMessage(`{"value":"changed"}`)), ErrConflict)
}
