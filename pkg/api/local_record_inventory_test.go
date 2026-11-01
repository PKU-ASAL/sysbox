package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/stretchr/testify/require"
)

func TestLocalInventorySkipsOrdinaryFilesAtWildcardDirectoryLevel(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "field.sysbox.hcl"), []byte("config"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "lab", "sessions"), 0700))
	expected := filepath.Join(root, "lab", "sessions", "session.json")
	require.NoError(t, os.WriteFile(expected, []byte("{}"), 0600))
	files, err := controlstore.LocalRecordPaths(filepath.Join(root, "*", "sessions", "*.json"))
	require.NoError(t, err)
	require.Equal(t, []string{expected}, files)
	_, err = controlstore.LocalRecordPaths(filepath.Join(root, "field.sysbox.hcl", "*.json"))
	require.Error(t, err, "explicit non-directory remains invalid")
}
