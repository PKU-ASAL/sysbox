package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

func cacheHostID() (string, error) {
	raw, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", ErrInvalidRecord
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(id))), nil
}

// The trigger protects even lifecycle writers that do not know this protocol.
// Deployments must still restrict DDL/administrative access to trusted operators.
func installCacheFence(ctx context.Context, c *sql.Conn) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sysbox_cache_deletions (id TEXT PRIMARY KEY, host TEXT NOT NULL, plan JSONB NOT NULL, state TEXT NOT NULL CHECK(state IN ('pending','done')))`,
		`ALTER TABLE sysbox_cache_deletions ADD COLUMN IF NOT EXISTS outcome TEXT`,
		`CREATE OR REPLACE FUNCTION sysbox_cache_write_guard() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE blocked boolean; BEGIN IF current_setting('transaction_isolation') <> 'read committed' THEN RAISE EXCEPTION 'cache publication requires read committed isolation' USING ERRCODE='55000'; END IF; EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.sysbox_cache_deletions WHERE state = %L)', TG_TABLE_SCHEMA, 'pending') INTO blocked; IF blocked THEN RAISE EXCEPTION 'cache deletion reservation freezes lifecycle catalog' USING ERRCODE='55000'; END IF; RETURN NULL; END $$`,
		`DROP TRIGGER IF EXISTS sysbox_cache_write_guard ON sysbox_lifecycle_aggregates`,
		`CREATE TRIGGER sysbox_cache_write_guard BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON sysbox_lifecycle_aggregates FOR EACH STATEMENT EXECUTE FUNCTION sysbox_cache_write_guard()`,
	}
	for _, s := range statements {
		if _, err := c.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	var exists bool
	if err := c.QueryRowContext(ctx, "SELECT to_regclass('sysbox_global_revisions') IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: global revision registry must be initialized before PostgreSQL cache apply", ErrConflict)
	}
	for _, statement := range []string{
		"DROP TRIGGER IF EXISTS sysbox_cache_write_guard ON sysbox_global_revisions",
		"CREATE TRIGGER sysbox_cache_write_guard BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON sysbox_global_revisions FOR EACH STATEMENT EXECUTE FUNCTION sysbox_cache_write_guard()",
	} {
		if _, err := c.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
func (r *Repository) reservePostgresCache(ctx context.Context, plan CacheGCPlan) error {
	b, ok := r.backend.(*sqlBackend)
	if !ok || !b.postgres || r.boundTopology != "" {
		return ErrUnsupportedVersion
	}
	host, err := cacheHostID()
	if err != nil {
		return err
	}
	return r.cacheCatalogTransaction(ctx, true, func(c *sql.Conn, all []*aggregate, digest string) error {
		current, err := cachePlan(all, digest, plan.Root, plan.Path)
		if err != nil {
			return err
		}
		if current != plan {
			return ErrConflict
		}
		if err = installCacheFence(ctx, c); err != nil {
			return err
		}
		var occupied bool
		if err = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sysbox_cache_deletions WHERE state='pending' OR id=$1)", plan.ID).Scan(&occupied); err != nil {
			return err
		}
		if occupied {
			return ErrConflict
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, "INSERT INTO sysbox_cache_deletions(id,host,plan,state) VALUES($1,$2,$3::jsonb,'pending')", plan.ID, host, string(raw))
		return err
	})
}
func verifyCacheDone(plan CacheGCPlan) error {
	if len(plan.ID) != 64 || strings.Trim(plan.ID, "0123456789abcdef") != "" || !filepath.IsAbs(plan.Root) || !filepath.IsLocal(plan.Path) {
		return ErrInvalidRecord
	}
	real, err := filepath.EvalSymlinks(plan.Root)
	if err != nil || real != plan.Root {
		return ErrConflict
	}
	journal := filepath.Join(plan.Root, ".sysbox-gc")
	if _, err = os.Lstat(filepath.Join(journal, plan.ID+".aborted")); !os.IsNotExist(err) {
		return ErrConflict
	}
	for _, path := range []string{plan.Root, journal, filepath.Join(journal, plan.ID+".done")} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return ErrConflict
		}
		if path != filepath.Join(journal, plan.ID+".done") && !info.IsDir() {
			return ErrConflict
		}
		if path == filepath.Join(journal, plan.ID+".done") && (!info.Mode().IsRegular() || st.Nlink != 1) {
			return ErrConflict
		}
	}
	raw, err := os.ReadFile(filepath.Join(journal, plan.ID+".done"))
	if err != nil {
		return err
	}
	var recorded CacheGCPlan
	if json.Unmarshal(raw, &recorded) != nil || recorded != plan {
		return ErrConflict
	}
	if _, err = os.Lstat(filepath.Join(journal, plan.ID+".intent")); !os.IsNotExist(err) {
		return ErrConflict
	}
	if _, err = os.Lstat(filepath.Join(plan.Root, plan.Path)); !os.IsNotExist(err) {
		return ErrConflict
	}
	return nil
}
func (r *Repository) releasePostgresCache(ctx context.Context, plan CacheGCPlan) error {
	if err := verifyCacheDone(plan); err != nil {
		return err
	}
	b, ok := r.backend.(*sqlBackend)
	if !ok || !b.postgres || r.boundTopology != "" {
		return ErrUnsupportedVersion
	}
	host, err := cacheHostID()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	_, err = b.db.ExecContext(ctx, "ALTER TABLE sysbox_cache_deletions ADD COLUMN IF NOT EXISTS outcome TEXT")
	if err != nil {
		return err
	}
	result, err := b.db.ExecContext(ctx, "UPDATE sysbox_cache_deletions SET state='done',outcome='deleted' WHERE id=$1 AND host=$2 AND plan=$3::jsonb AND (outcome IS NULL OR outcome='deleted')", plan.ID, host, string(raw))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// ReconcileCacheGC never deletes a file. The same host may finish releasing a
// committed reservation only when the durable local done receipt proves its
// exact unlink completed. Missing/partial receipts remain pending indefinitely.
func (r *Repository) ReconcileCacheGC(ctx context.Context, plan CacheGCPlan) error {
	if err := verifyCacheDone(plan); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(plan.Root, ".sysbox-gc", "lock"))
	defer lock.Close()
	held, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !held {
		return ErrConflict
	}
	return r.releasePostgresCache(ctx, plan)
}
