package guesttask

import (
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"os"
	"testing"
)

func TestFileRebootScopeRejectsExternalAndUnknownFilesystems(t *testing.T) {
	for _, kind := range []int64{unix.NFS_SUPER_MAGIC, unix.FUSE_SUPER_MAGIC, unix.EXT4_SUPER_MAGIC, unix.OVERLAYFS_SUPER_MAGIC, 0} {
		require.False(t, fileRebootSafeFilesystem(kind))
	}
	require.True(t, fileRebootSafeFilesystem(unix.TMPFS_MAGIC))
	dir, err := os.Open(t.TempDir())
	require.NoError(t, err)
	defer dir.Close()
	var st unix.Statfs_t
	require.NoError(t, unix.Fstatfs(int(dir.Fd()), &st))
	if st.Type != unix.TMPFS_MAGIC {
		require.Error(t, validateFileRebootScope(dir, "guest-tmpfs"))
	}
	require.NoError(t, validateFileRebootScope(dir, ""))
}
