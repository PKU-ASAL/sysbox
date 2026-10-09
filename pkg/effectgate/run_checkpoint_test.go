package effectgate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunCheckpointImmutableAcrossRenewalAndColdRead(t *testing.T) {
	gate, signed, key := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, signed, "effect")
	require.NoError(t, err)
	checkpoint := json.RawMessage(`{"resources":"complete","children":[]}`)
	require.NoError(t, gate.CheckpointRun(ctx, signed, "effect", checkpoint))
	require.NoError(t, gate.CheckpointRun(ctx, signed, "effect", checkpoint))
	require.ErrorIs(t, gate.CheckpointRun(ctx, signed, "effect", json.RawMessage(`{"resources":"different"}`)), ErrFenced)
	renewed := extendedRunGrant(t, key, signed, time.Minute)
	require.NoError(t, gate.RenewRun(ctx, signed, renewed, "effect"))
	require.ErrorIs(t, gate.CheckpointRun(ctx, signed, "effect", checkpoint), ErrFenced)
	cold := *gate
	got, err := cold.ReadRun(ctx, signed, "effect")
	require.NoError(t, err)
	require.JSONEq(t, string(checkpoint), string(got.Checkpoint))
	require.Equal(t, RunIntent, got.State)
}
