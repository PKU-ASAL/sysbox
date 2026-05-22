package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/runtime"
)

func TestLocalAPIStorePersistsRunCheckpointAndHealth(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()

	run := Run{ID: "run-1", Topology: "mixed", Op: "apply", Status: RunRunning, StartedAt: time.Now().UTC()}
	require.NoError(t, store.SaveRun(ctx, run))
	runs, err := store.LoadRuns(ctx)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "run-1", runs[0].ID)

	cp := runtime.OperationCheckpoint{RunID: "run-1", Topology: "mixed", Operation: "apply", Status: runtime.OperationStarted}
	require.NoError(t, store.SaveCheckpoint(ctx, "mixed", "run-1", cp))
	gotCP, err := store.LoadCheckpoint(ctx, "mixed", "run-1")
	require.NoError(t, err)
	require.Equal(t, "run-1", gotCP.RunID)

	snap := HealthSnapshot{Topology: "mixed", Observed: time.Now().UTC(), Policy: SupervisorPolicyObserveOnly}
	require.NoError(t, store.SaveHealth(ctx, "mixed", snap))
	gotHealth, err := store.LoadHealth(ctx, "mixed")
	require.NoError(t, err)
	require.Equal(t, "mixed", gotHealth.Topology)
}
