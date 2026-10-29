package api

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/stretchr/testify/require"
)

func TestFenceDeleteRunHistoryUsesAtomicReplacement(t *testing.T) {
	ctx := context.Background()
	s := &controlstore.Local{RunsDir: t.TempDir()}
	run := controlplane.Run{ID: "run", Topology: "lab", Status: controlplane.RunRunning}
	require.NoError(t, s.SaveRunUnlocked(ctx, run))
	path := filepath.Join(s.RunsDir, "lab", "runs.jsonl")
	prior, err := os.Open(path)
	require.NoError(t, err)
	defer prior.Close()
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	run.Status = controlplane.RunFailed
	require.NoError(t, s.SaveFenceDeleteRun(ctx, run))
	old, err := io.ReadAll(prior)
	require.NoError(t, err)
	require.Equal(t, before, old, "original inode must not receive a potentially torn append")
	history, err := controlstore.ReadLocalHistory[controlplane.Run](ctx, path)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, controlplane.RunFailed, history[1].Status)
	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0600))
	require.Error(t, s.SaveFenceDeleteRun(ctx, run))
	unchanged, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "{broken", string(unchanged))
}
