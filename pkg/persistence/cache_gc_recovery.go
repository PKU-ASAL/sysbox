package persistence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

func cacheRecoveryPaths(plan CacheGCPlan) (string, error) {
	if len(plan.ID) != 64 || strings.Trim(plan.ID, "0123456789abcdef") != "" || !filepath.IsAbs(plan.Root) || filepath.Clean(plan.Root) != plan.Root {
		return "", ErrInvalidRecord
	}
	parts := strings.Split(plan.Path, string(filepath.Separator))
	if len(parts) != 2 || len(parts[0]) != 64 || strings.Trim(parts[0], "0123456789abcdef") != "" || parts[1] == "" || strings.HasPrefix(parts[1], ".") || !filepath.IsLocal(plan.Path) {
		return "", ErrInvalidRecord
	}
	real, err := filepath.EvalSymlinks(plan.Root)
	if err != nil || real != plan.Root {
		return "", ErrConflict
	}
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	for _, p := range []string{plan.Root, journal, filepath.Join(plan.Root, parts[0])} {
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Geteuid()) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return "", ErrConflict
		}
		if (p == plan.Root || p == journal) && info.Mode().Perm()&0077 != 0 {
			return "", ErrConflict
		}
	}
	return journal, nil
}

// RecoverCacheGCIntent never unlinks a resource. Acquiring the original private
// host lock excludes every deleter using this journal; an exact durable intent
// and fresh absence can therefore complete an interrupted receipt publication.
// A surviving/replaced target is always a conflict, never permission to retry.
func (r *Repository) RecoverCacheGCIntent(ctx context.Context, plan CacheGCPlan) error {
	b, ok := r.backend.(*sqlBackend)
	if !ok || r.boundTopology != "" {
		return ErrUnsupportedVersion
	}
	journal, err := cacheRecoveryPaths(plan)
	if err != nil {
		return err
	}
	lock := flock.New(filepath.Join(journal, "lock"))
	defer lock.Close()
	held, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !held {
		return ErrConflict
	}
	if b.postgres {
		host, err := cacheHostID()
		if err != nil {
			return err
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		var matches bool
		if err = b.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sysbox_cache_deletions WHERE id=$1 AND host=$2 AND plan=$3::jsonb AND state IN ('pending','done'))", plan.ID, host, string(raw)).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return ErrConflict
		}
	}
	if _, err = os.Lstat(filepath.Join(journal, plan.ID+".aborted")); !os.IsNotExist(err) {
		return ErrConflict
	}
	done := filepath.Join(journal, plan.ID+".done")
	if _, err = os.Lstat(done); err == nil {
		if err = verifyCacheDone(plan); err != nil {
			return err
		}
		if b.postgres {
			return r.releasePostgresCache(ctx, plan)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	intent := filepath.Join(journal, plan.ID+".intent")
	info, err := os.Lstat(intent)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Nlink != 1 || info.Size() > 65536 {
		return ErrConflict
	}
	raw, err := os.ReadFile(intent)
	if err != nil {
		return err
	}
	var original CacheGCPlan
	if json.Unmarshal(raw, &original) != nil || original != plan {
		return ErrConflict
	}
	if _, err = os.Lstat(filepath.Join(plan.Root, plan.Path)); !os.IsNotExist(err) {
		return ErrConflict
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = syncCacheDir(filepath.Dir(filepath.Join(plan.Root, plan.Path))); err != nil {
		return err
	}
	if err = os.Rename(intent, done); err != nil {
		return err
	}
	if err = syncCacheDir(journal); err != nil {
		return err
	}
	if b.postgres {
		return r.releasePostgresCache(ctx, plan)
	}
	return nil
}
