package persistence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteRepositoryCreatesPrivateAuthorityFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical.db")
	repository, err := OpenSQLite(path)
	require.NoError(t, err)
	defer repository.Close()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil {
			require.Zero(t, info.Mode().Perm()&0077)
		}
	}
}
