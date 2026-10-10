package persistence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5"
)

// LockExecution serializes cooperating lifecycle executors, independently of
// aggregate transactions. It is not an effect fence: losing a process or SQL
// session does not prove that its outstanding provider requests have finished.
// The caller must hold the lock until its execution has returned, even when
// ctx is canceled. The returned release function is safe to call repeatedly.
func (r *Repository) LockExecution(ctx context.Context, topology string) (func(), error) {
	if !topologyName.MatchString(topology) || r.boundTopology != "" && r.boundTopology != topology {
		return nil, fmt.Errorf("invalid execution lock topology %q", topology)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch b := r.backend.(type) {
	case *fileBackend:
		return lockExecutionFile(ctx, filepath.Join(b.root, topology+".execution.lock"))
	case *sqlBackend:
		if b.postgres {
			return lockExecutionPostgres(ctx, b.dsn, topology)
		}
		return lockExecutionFile(ctx, filepath.Join(b.path+".execution", topology+".lock"))
	default:
		return nil, fmt.Errorf("repository backend does not support execution locking")
	}
}

func lockExecutionFile(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	locked, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil || !locked {
		_ = lock.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = lock.Close() }) }, nil
}

func lockExecutionPostgres(ctx context.Context, dsn, topology string) (func(), error) {
	// Waiting executors must not exhaust the aggregate transaction pool.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			closing, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = conn.Close(closing)
		})
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var locked bool
		// The resolved relation identifies the storage domain; current_schema()
		// can differ between connections that resolve this same table.
		err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(
			('sysbox_lifecycle_aggregates'::regclass::oid::bigint << 32) |
			(hashtext('sysbox-execution:' || $1::text)::bigint & 4294967295))`, topology).Scan(&locked)
		if err != nil {
			release()
			return nil, err
		}
		if locked {
			return release, nil
		}
		select {
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
