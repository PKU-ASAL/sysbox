package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceDeletePreservesCorruptState(t *testing.T) {
	runs, work := t.TempDir(), t.TempDir()
	s := NewServer(runs, work)
	path := filepath.Join(runs, "broken", "state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{corrupt"), 0o600))
	require.Error(t, s.workspaceService().Delete(context.Background(), "broken", false))
	_, err := os.Stat(path)
	require.NoError(t, err)
}
