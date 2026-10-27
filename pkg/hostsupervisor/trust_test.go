package hostsupervisor

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustRejectsWritableAndSymlinkPaths(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(path, []byte("private"), 0600))
	require.NoError(t, ValidateOwnedPath(path, true))
	require.NoError(t, os.Chmod(path, 0640))
	require.Error(t, ValidateOwnedPath(path, true))
	require.NoError(t, ValidateOwnedPath(path, false))
	require.NoError(t, os.Chmod(path, 0660))
	require.Error(t, ValidateOwnedPath(path, false))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(path, link))
	require.Error(t, ValidateOwnedPath(link, false))
	parent := filepath.Join(dir, "worker")
	require.NoError(t, os.Mkdir(parent, 0770))
	require.NoError(t, os.Chmod(parent, 0770))
	require.Error(t, EnsureOwnedDirectory(filepath.Join(parent, "supervisor"), 0700))
}

func TestTrustPrivateDirectoryAndForeignOwner(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0700))
	dir := filepath.Join(parent, "private")
	require.NoError(t, EnsureOwnedDirectory(dir, 0700))
	require.NoError(t, ValidateOwnedPath(dir, true))
	if os.Geteuid() != 0 {
		t.Skip("foreign ownership fixture requires root")
	}
	require.NoError(t, os.Chown(dir, 65534, 65534))
	require.Error(t, ValidateOwnedPath(dir, true))
}

func TestCanonicalRepositoryRejectsWorkerWritableState(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	base := filepath.Join(dir, "state.json")
	root := base + ".lifecycle"
	require.NoError(t, os.Mkdir(root, 0700))
	record := filepath.Join(root, "lab.json")
	require.NoError(t, os.WriteFile(record, []byte("{}"), 0600))
	require.NoError(t, validateRepositoryTrust("", base, "lab"))
	require.NoError(t, os.Chmod(record, 0660))
	require.Error(t, validateRepositoryTrust("", base, "lab"))
	sqlite := filepath.Join(dir, "db.sqlite")
	require.NoError(t, os.WriteFile(sqlite, nil, 0600))
	require.NoError(t, validateRepositoryTrust("sqlite://"+sqlite, "", "lab"))
	require.NoError(t, os.Chmod(sqlite, 0660))
	require.Error(t, validateRepositoryTrust("sqlite://"+sqlite, "", "lab"))
}
