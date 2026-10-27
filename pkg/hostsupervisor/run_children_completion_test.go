package hostsupervisor

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRunEndTimeIncludesChildCompletion(t *testing.T) {
	children := &RunChildren{}
	before := time.Now()
	run := &controlplane.Run{Status: controlplane.RunDone, EndedAt: before.Add(-time.Minute)}
	require.NoError(t, children.Finish(context.Background(), run))
	require.False(t, run.EndedAt.Before(before))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unchanged := *run
	require.Error(t, (&RunChildren{}).Finish(ctx, run))
	require.Equal(t, unchanged, *run)
}
