package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// The lock file lives outside every deletable topology directory. Never unlink
// it: concurrent processes must keep locking the same inode across deletion.
func (s *WorkspaceService) lockPublication(ctx context.Context, topology string) (func(), error) {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.runsDir, ".workspace-locks")
	if err := mkdirWorkspaceDirectories(dir); err != nil {
		return nil, fmt.Errorf("create workspace lock directory: %w", err)
	}
	lease := flock.New(filepath.Join(dir, topology+".lock"))
	locked, err := lease.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("lock workspace publication: %w", err)
	}
	if !locked {
		_ = lease.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("workspace publication lock unavailable")
	}
	if err := ctx.Err(); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return func() { _ = lease.Close() }, nil
}

func mkdirWorkspaceDirectories(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return syncWorkspaceParents(path)
}

// Persist both directory contents and each directory's entry in its parent.
// Include existing ancestors: an earlier attempt may have created them and
// then failed before syncing, so existence alone is not a durability barrier.
func syncWorkspaceParents(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		if err := syncDirectory(abs); err != nil {
			return err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil
		}
		abs = parent
	}
}
