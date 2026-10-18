package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFenceDeleteRunHistoryUsesAtomicReplacement(t *testing.T) {
	ctx := context.Background()
	s := &localAPIStore{runsDir: t.TempDir()}
	run := controlplane.Run{ID: "run", Topology: "lab", Status: controlplane.RunRunning}
	require.NoError(t, s.saveRunUnlocked(ctx, run))
	path := filepath.Join(s.runsDir, "lab", "runs.jsonl")
	prior, err := os.Open(path)
	require.NoError(t, err)
	defer prior.Close()
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	run.Status = controlplane.RunFailed
	require.NoError(t, s.saveFenceDeleteRun(ctx, run))
	old, err := io.ReadAll(prior)
	require.NoError(t, err)
	require.Equal(t, before, old, "original inode must not receive a potentially torn append")
	history, err := readLocalHistory[controlplane.Run](ctx, path)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, controlplane.RunFailed, history[1].Status)
	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0600))
	require.Error(t, s.saveFenceDeleteRun(ctx, run))
	unchanged, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "{broken", string(unchanged))
}
