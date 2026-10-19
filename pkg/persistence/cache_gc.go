package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

// CacheGCPlan covers one private host cache file and the entire shared SQL
// lifecycle catalog. Every writer using this cache must participate in source
// publication. Deployment enrollment, not this API, establishes that boundary.
type CacheGCPlan struct {
	ID            string `json:"id"`
	Root          string `json:"root"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	CatalogSHA256 string `json:"catalog_sha256"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
	Size          int64  `json:"size"`
	Modified      int64  `json:"modified_ns"`
}

// withCacheCatalog prevents even new topology insertion through the complete
// check/delete interval. Per-row locks alone would miss phantom enrollment.
func (r *Repository) withCacheCatalog(ctx context.Context, fn func([]*aggregate, string) error) error {
	return r.cacheCatalogTransaction(ctx, false, func(_ *sql.Conn, all []*aggregate, digest string) error { return fn(all, digest) })
}
func (r *Repository) cacheCatalogTransaction(ctx context.Context, commit bool, fn func(*sql.Conn, []*aggregate, string) error) error {
	b, ok := r.backend.(*sqlBackend)
	if !ok || r.boundTopology != "" {
		return ErrUnsupportedVersion
	}
	conn, err := b.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	begin := "BEGIN IMMEDIATE"
	if b.postgres {
		begin = "BEGIN"
	}
	if _, err = conn.ExecContext(ctx, begin); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if b.postgres {
		if _, err = conn.ExecContext(ctx, "LOCK TABLE sysbox_lifecycle_aggregates IN EXCLUSIVE MODE"); err != nil {
			return err
		}
	}
	var legacy bool
	legacyQuery := `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='sysbox_state')`
	if b.postgres {
		legacyQuery = `SELECT to_regclass('sysbox_state') IS NOT NULL`
	}
	if err = conn.QueryRowContext(ctx, legacyQuery).Scan(&legacy); err != nil {
		return err
	}
	if legacy {
		return fmt.Errorf("%w: legacy catalog is not covered", ErrConflict)
	}
	rows, err := conn.QueryContext(ctx, "SELECT topology,data FROM sysbox_lifecycle_aggregates ORDER BY topology")
	if err != nil {
		return err
	}
	defer rows.Close()
	var all []*aggregate
	digest := sha256.New()
	for rows.Next() {
		var name string
		var raw []byte
		if err = rows.Scan(&name, &raw); err != nil {
			return err
		}
		a, err := decodeAggregate(raw)
		if err != nil {
			return err
		}
		if err = validateAggregate(a, name); err != nil {
			return err
		}
		if len(a.GCSources) == 0 || gcPending(a) {
			return fmt.Errorf("%w: unenrolled or deleting topology %s", ErrConflict, name)
		}
		if _, err = gcReferences(a); err != nil {
			return err
		}
		all = append(all, a)
		encoded, _ := json.Marshal(struct {
			Name string
			Data json.RawMessage
		}{name, raw})
		_, _ = digest.Write(encoded)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("%w: empty catalog is not enrollment evidence", ErrConflict)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = fn(conn, all, fmt.Sprintf("%x", digest.Sum(nil))); err != nil {
		return err
	}
	if commit {
		if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		committed = true
	}
	return nil
}
func privateCacheFile(root, relative string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsLocal(relative) {
		return "", nil, ErrInvalidRecord
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 2 || len(parts[0]) != 64 || strings.Trim(parts[0], "0123456789abcdef") != "" || parts[1] == "" || strings.HasPrefix(parts[1], ".") {
		return "", nil, ErrInvalidRecord
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real != root {
		return "", nil, ErrConflict
	}
	path := root
	for i, part := range append([]string{""}, parts...) {
		if part != "" {
			path = filepath.Join(path, part)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", nil, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return "", nil, ErrConflict
		}
		if i == 0 && info.Mode().Perm()&0077 != 0 {
			return "", nil, fmt.Errorf("cache GC requires a private root")
		}
		if i < len(parts) {
			if !info.IsDir() {
				return "", nil, ErrConflict
			}
		} else {
			if !info.Mode().IsRegular() || st.Nlink != 1 {
				return "", nil, ErrConflict
			}
			return path, info, nil
		}
	}
	return "", nil, ErrInvalidRecord
}
func cacheReferenced(a *aggregate, needles []string) (bool, error) {
	// State and action private attributes may retain paths/digests even if a
	// producer domain omits them. Conservatively protect any matching string.
	copy := *a
	copy.Snapshots = nil
	raw, err := json.Marshal(copy)
	if err != nil {
		return true, err
	}
	for _, needle := range needles {
		encoded, _ := json.Marshal(needle)
		if bytes.Contains(raw, encoded[1:len(encoded)-1]) {
			return true, nil
		}
	}
	for id, snap := range a.Snapshots {
		content, err := decodeSnapshot(id, snap)
		if err != nil {
			return true, err
		}
		yes, err := cacheReferenced(content, needles)
		if err != nil || yes {
			return yes, err
		}
	}
	return false, nil
}
func cachePlan(all []*aggregate, catalog, root, relative string) (CacheGCPlan, error) {
	var zero CacheGCPlan
	path, info, err := privateCacheFile(root, relative)
	if err != nil {
		return zero, err
	}
	file, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return zero, ErrConflict
	}
	sum := sha256.New()
	if _, err = io.Copy(sum, file); err != nil {
		return zero, err
	}
	digest := fmt.Sprintf("%x", sum.Sum(nil))
	pathDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(path)))
	for _, a := range all {
		yes, err := cacheReferenced(a, []string{digest, pathDigest, path, relative})
		if err != nil {
			return zero, err
		}
		if yes {
			return zero, fmt.Errorf("%w: cache file is referenced", ErrConflict)
		}
	}
	st := info.Sys().(*syscall.Stat_t)
	plan := CacheGCPlan{Root: root, Path: relative, SHA256: digest, CatalogSHA256: catalog, Device: uint64(st.Dev), Inode: st.Ino, Size: info.Size(), Modified: info.ModTime().UnixNano()}
	raw, _ := json.Marshal(plan)
	plan.ID = fmt.Sprintf("%x", sha256.Sum256(raw))
	return plan, nil
}
func (r *Repository) PlanCacheGC(ctx context.Context, root, relative string) (CacheGCPlan, error) {
	var out CacheGCPlan
	err := r.withCacheCatalog(ctx, func(all []*aggregate, digest string) error {
		var err error
		out, err = cachePlan(all, digest, root, relative)
		return err
	})
	if err != nil {
		return CacheGCPlan{}, err
	}
	return out, nil
}
func syncCacheDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (r *Repository) ApplyCacheGC(ctx context.Context, plan CacheGCPlan) error {
	b, ok := r.backend.(*sqlBackend)
	if !ok {
		return ErrUnsupportedVersion
	}
	if _, _, err := privateCacheFile(plan.Root, plan.Path); err != nil {
		return err
	}
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	if err := os.Mkdir(journal, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(journal)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrConflict
	}
	lock := flock.New(filepath.Join(journal, "lock"))
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ErrConflict
	}
	deleteFile := func(all []*aggregate, digest string) error {
		current, err := cachePlan(all, digest, plan.Root, plan.Path)
		if err != nil {
			return err
		}
		if current != plan {
			return ErrConflict
		}
		entries, err := os.ReadDir(journal)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".intent") {
				return fmt.Errorf("%w: unresolved cache deletion", ErrConflict)
			}
		}
		if _, err = os.Stat(filepath.Join(journal, plan.ID+".done")); !os.IsNotExist(err) {
			return ErrConflict
		}
		if _, err = os.Lstat(filepath.Join(journal, plan.ID+".aborted")); !os.IsNotExist(err) {
			return ErrConflict
		}
		intent := filepath.Join(journal, plan.ID+".intent")
		f, err := os.OpenFile(intent, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(plan)
		_, writeErr := f.Write(raw)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err = syncCacheDir(journal); err != nil {
			return err
		}
		if err = syncCacheDir(plan.Root); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		// Recheck after durable intent. All participating source writers remain
		// blocked by the catalog transaction, including new topology enrollment.
		checked, err := cachePlan(all, digest, plan.Root, plan.Path)
		if err != nil {
			return err
		}
		if checked != plan {
			return ErrConflict
		}
		target := filepath.Join(plan.Root, plan.Path)
		if err = os.Remove(target); err != nil {
			return err
		}
		if err = syncCacheDir(filepath.Dir(target)); err != nil {
			return err
		}
		if err = os.Rename(intent, filepath.Join(journal, plan.ID+".done")); err != nil {
			return err
		}
		return syncCacheDir(journal)
	}
	if b.postgres {
		if err := r.reservePostgresCache(ctx, plan); err != nil {
			return err
		}
		// No write can resume after this committed reservation even if this
		// connection disappears. Never replay an existing pending reservation.
		if err := deleteFile(nil, plan.CatalogSHA256); err != nil {
			return err
		}
		return r.releasePostgresCache(ctx, plan)
	}
	return r.withCacheCatalog(ctx, deleteFile)
}
