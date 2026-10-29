package api

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalInventorySkipsOrdinaryFilesAtWildcardDirectoryLevel(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "field.sysbox.hcl"), []byte("config"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "lab", "sessions"), 0700))
	expected := filepath.Join(root, "lab", "sessions", "session.json")
	require.NoError(t, os.WriteFile(expected, []byte("{}"), 0600))
	files, err := localRecordPaths(filepath.Join(root, "*", "sessions", "*.json"))
	require.NoError(t, err)
	require.Equal(t, []string{expected}, files)
	_, err = localRecordPaths(filepath.Join(root, "field.sysbox.hcl", "*.json"))
	require.Error(t, err, "explicit non-directory remains invalid")
}
