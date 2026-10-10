package persistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type sqlBackend struct {
	db       *sql.DB
	postgres bool
	path     string
	dsn      string
}

func OpenSQLite(path string) (*Repository, error) {
	return openSQLite(path, true)
}

func openSQLite(path string, initialize bool) (*Repository, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("durable SQLite repository requires a filesystem path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if initialize {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	} else if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	// SQLite and its execution sidecar must use the same canonical file path.
	// Otherwise a symlink alias could acquire an independent execution lock.
	if _, err := os.Lstat(path); err == nil {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
	} else if os.IsNotExist(err) {
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		path = filepath.Join(parent, filepath.Base(path))
	} else {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	if initialize {
		q.Add("_pragma", "journal_mode(WAL)")
	} else {
		q.Set("mode", "rw") // open an existing database without creating one
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if initialize {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS sysbox_lifecycle_aggregates (topology TEXT PRIMARY KEY, data BLOB NOT NULL)`)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Repository{backend: &sqlBackend{db: db, path: path}}, nil
}
func OpenPostgres(ctx context.Context, dsn string) (*Repository, error) {
	return openPostgres(ctx, dsn, true)
}

func openPostgres(ctx context.Context, dsn string, initialize bool) (*Repository, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if initialize {
		err = initializePostgresLifecycle(ctx, db)
	} else {
		err = db.PingContext(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Repository{backend: &sqlBackend{db: db, postgres: true, dsn: dsn}}, nil
}

func initializePostgresLifecycle(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// IF NOT EXISTS alone does not serialize concurrent PostgreSQL catalog/type
	// creation. Keep schema-local bootstrap under one cross-process transaction.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()), hashtext('sysbox-lifecycle-schema'))`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sysbox_lifecycle_aggregates (topology TEXT PRIMARY KEY, data JSONB NOT NULL)`); err != nil {
		return err
	}
	return tx.Commit()
}
func (b *sqlBackend) close() error { return b.db.Close() }
func (b *sqlBackend) keys(ctx context.Context) ([]string, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT topology FROM sysbox_lifecycle_aggregates ORDER BY topology`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
func (b *sqlBackend) name() string {
	if b.postgres {
		return "postgres-aggregate"
	}
	return "sqlite-aggregate"
}
func (b *sqlBackend) update(ctx context.Context, key string, fn func(*aggregate) error) error {
	return b.updateDecoded(ctx, key, decodeAggregate, fn)
}

func (b *sqlBackend) updateDecoded(ctx context.Context, key string, decode func([]byte) (*aggregate, error), fn func(*aggregate) error) error {
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
		// A failed response does not prove BEGIN had no effect on the server.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
				// Never pool a session whose transaction state is unknown.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	// A legacy state row is not an empty topology. Reject it before creating a
	// new aggregate, even when API metadata shares this database.
	var legacy bool
	legacyQuery := `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='sysbox_state')`
	if b.postgres {
		legacyQuery = `SELECT to_regclass('sysbox_state') IS NOT NULL`
	}
	if err := conn.QueryRowContext(ctx, legacyQuery).Scan(&legacy); err != nil {
		return err
	}
	if legacy {
		var exists bool
		q := `SELECT EXISTS(SELECT 1 FROM sysbox_state WHERE topology=?)`
		if b.postgres {
			q = `SELECT EXISTS(SELECT 1 FROM sysbox_state WHERE topology=$1)`
		}
		if err := conn.QueryRowContext(ctx, q, key).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: legacy topology %s must be explicitly rebuilt", ErrUnsupportedVersion, key)
		}
	}
	initial, err := json.Marshal(newAggregate())
	if err != nil {
		return err
	}
	insert := `INSERT INTO sysbox_lifecycle_aggregates (topology,data) VALUES (?,?) ON CONFLICT(topology) DO NOTHING`
	query := `SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?`
	update := `UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?`
	var seed any = initial
	if b.postgres {
		insert = `INSERT INTO sysbox_lifecycle_aggregates (topology,data) VALUES ($1,$2::jsonb) ON CONFLICT(topology) DO NOTHING`
		query = `SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE`
		update = `UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2`
		seed = string(initial)
	}
	if _, err = conn.ExecContext(ctx, insert, key, seed); err != nil {
		return err
	}
	var raw []byte
	if err = conn.QueryRowContext(ctx, query, key).Scan(&raw); err != nil {
		return err
	}
	a, err := decode(raw)
	if err != nil {
		return err
	}
	before, _ := json.Marshal(a)
	if err = fn(a); err != nil {
		return err
	}
	next, err := json.Marshal(a)
	if err != nil {
		return err
	}
	var payload any = next
	if b.postgres {
		payload = string(next)
	}
	if string(before) != string(next) {
		if _, err = conn.ExecContext(ctx, update, payload, key); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit aggregate: %w", err)
	}
	committed = true
	return nil
}
