package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRestartKeepsCompletedRunInList(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	s := NewServer(dir, work)
	ctx := context.Background()
	run := controlplane.Run{ID: "run", Topology: "lab", Op: "apply", Operation: "apply", Revision: "rev", OperationKey: "key", QueuedAt: time.Now().UTC()}
	run.MarkAssigned("host", time.Now().UTC())
	_, _, err := s.apiStore.CreateRunDispatch(ctx, incarnationDispatch(run))
	require.NoError(t, err)
	run.Status = controlplane.RunDone
	run.EndedAt = time.Now().UTC()
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	cold := NewServer(dir, work)
	list := cold.jobs.list("lab")
	require.Len(t, list, 1)
	require.Equal(t, controlplane.RunDone, list[0].Status)
}
