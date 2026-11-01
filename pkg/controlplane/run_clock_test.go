package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHostWaitsForCanonicalRunStartWithoutChangingEvidence(t *testing.T) {
	future := time.Now().Add(40 * time.Millisecond)
	require.NoError(t, WaitForCanonicalRunStart(context.Background(), future))
	require.False(t, time.Now().Before(future))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, WaitForCanonicalRunStart(ctx, time.Now().Add(time.Second)))
	require.Error(t, WaitForCanonicalRunStart(context.Background(), time.Now().Add(time.Minute)))
	require.NoError(t, WaitForCanonicalRunStart(context.Background(), time.Now().Add(-time.Minute)))
}
