package agentexec

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestHostWaitsForCanonicalRunStartWithoutChangingEvidence(t *testing.T) {
	future := time.Now().Add(40 * time.Millisecond)
	require.NoError(t, waitForCanonicalRunStart(context.Background(), future))
	require.False(t, time.Now().Before(future))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, waitForCanonicalRunStart(ctx, time.Now().Add(time.Second)))
	require.Error(t, waitForCanonicalRunStart(context.Background(), time.Now().Add(time.Minute)))
	require.NoError(t, waitForCanonicalRunStart(context.Background(), time.Now().Add(-time.Minute)))
}
