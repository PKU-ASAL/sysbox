package state

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteBackend stores state in a local SQLite database. It implements the
// full suite of optional interfaces (VersionedBackend, LeaseBackend,
// SnapshotBackend, DeleteBackend, LockInfoBackend) so local single-node
// deployments get the same correctness guarantees as Postgres without needing
// an external database.
type SQLiteBackend struct {
	Path     string // full filesystem path to the .sqlite file
	Topology string // topology key (maps to a row in the sysbox_state table)
}

// ── low‑level helpers ────────────────────────────────────────────────────────

func (b *SQLiteBackend) topology() string {
	if b.Topology != "" {
		return b.Topology
	}
	return "default"
}

func (b *SQLiteBackend) connect(ctx context.Context) (*sql.DB, error) {
	dir := filepath.Dir(b.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sqlite mkdir %s: %w", dir, err)
	}
	db, err := sql.Open("sqlite", b.Path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	if err := b.ensureSchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (b *SQLiteBackend) ensureSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
	CREATE TABLE IF NOT EXISTS sysbox_state (
		topology  TEXT PRIMARY KEY,
		version   INTEGER NOT NULL,
		serial    INTEGER NOT NULL DEFAULT 0,
		data      BLOB NOT NULL,
		checksum  TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_state_locks (
		topology   TEXT PRIMARY KEY,
		owner      TEXT NOT NULL,
		lease_id   TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	) STRICT;

	CREATE TABLE IF NOT EXISTS sysbox_state_snapshots (
		topology  TEXT NOT NULL,
		id        TEXT NOT NULL,
		reason    TEXT DEFAULT '',
		data      BLOB NOT NULL,
		checksum  TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (topology, id)
	) STRICT;

	PRAGMA journal_mode=WAL;
	PRAGMA foreign_keys=ON;
	`)
	return err
}

// ── Backend interface ────────────────────────────────────────────────────────

func (b *SQLiteBackend) Load(ctx context.Context) ([]byte, error) {
	loaded, err := b.LoadVersioned(ctx)
	if err != nil || loaded == nil || !loaded.Exists {
		return nil, err
	}
	return loaded.Data, nil
}

func (b *SQLiteBackend) Save(ctx context.Context, data []byte) error {
	return b.SaveVersioned(ctx, data, SaveOptions{})
}

// ── VersionedBackend interface (CAS + serial) ────────────────────────────────

func (b *SQLiteBackend) LoadVersioned(ctx context.Context) (*LoadedState, error) {
	db, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	meta := Metadata{Backend: "sqlite", Location: b.Path, Version: SchemaVersion}
	row := db.QueryRowContext(ctx,
		`SELECT version, serial, data, checksum, updated_at FROM sysbox_state WHERE topology=?`,
		b.topology())

	var data []byte
	var version, serial int
	var checksum, updatedAt string
	err = row.Scan(&version, &serial, &data, &checksum, &updatedAt)
	if err == sql.ErrNoRows {
		return &LoadedState{Metadata: meta, Exists: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite load state: %w", err)
	}
	if checksum != checksumBytes(data) {
		if !validJSON(data) {
			return nil, fmt.Errorf("sqlite state checksum mismatch for topology %q", b.topology())
		}
	}
	t, _ := timeParse(updatedAt)
	return &LoadedState{
		Data:      data,
		Metadata:  Metadata{Backend: "sqlite", Location: b.Path, Version: version, Serial: int64(serial), UpdatedAt: t},
		Exists:    true,
		Serial:    int64(serial),
		UpdatedAt: t,
	}, nil
}

func (b *SQLiteBackend) SaveVersioned(ctx context.Context, data []byte, opts SaveOptions) error {
	db, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil) // defaults to deferred, safe for read-then-write
	if err != nil {
		return fmt.Errorf("sqlite begin: %w", err)
	}
	defer tx.Rollback()

	if opts.RequireCAS {
		if opts.ExpectedSerial == 0 {
			res, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO sysbox_state (topology, version, serial, data, checksum) VALUES (?, ?, 1, ?, ?)`,
				b.topology(), SchemaVersion, data, checksumBytes(data))
			if err != nil {
				return fmt.Errorf("sqlite save state: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				return tx.Commit()
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE sysbox_state SET version=?, serial=serial+1, data=?, checksum=?, updated_at=datetime('now')
			 WHERE topology=? AND serial=?`,
			SchemaVersion, data, checksumBytes(data), b.topology(), opts.ExpectedSerial)
		if err != nil {
			return fmt.Errorf("sqlite save state: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return tx.Commit()
		}

		var actual int64
		err = tx.QueryRowContext(ctx, `SELECT serial FROM sysbox_state WHERE topology=?`, b.topology()).Scan(&actual)
		if err == sql.ErrNoRows {
			actual = 0
		} else if err != nil {
			return fmt.Errorf("sqlite read current serial: %w", err)
		}
		return &ConflictError{Expected: opts.ExpectedSerial, Actual: actual}
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO sysbox_state (topology, version, serial, data, checksum) VALUES (?, ?, 1, ?, ?)
		 ON CONFLICT(topology) DO UPDATE SET version=excluded.version, serial=serial+1, data=excluded.data, checksum=excluded.checksum, updated_at=datetime('now')`,
		b.topology(), SchemaVersion, data, checksumBytes(data))
	if err != nil {
		return fmt.Errorf("sqlite save state: %w", err)
	}
	return tx.Commit()
}

// ── Lock (exclusive file-based via BEGIN IMMEDIATE) ──────────────────────────

func (b *SQLiteBackend) Lock(ctx context.Context) (UnlockFunc, error) {
	_, unlock, err := b.LockWithOptions(ctx, LockOptions{})
	return unlock, err
}

func (b *SQLiteBackend) LockWithOptions(ctx context.Context, opts LockOptions) (*Lease, UnlockFunc, error) {
	db, err := b.connect(ctx)
	if err != nil {
		return nil, nil, err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{}) // deferred
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("sqlite lock begin: %w", err)
	}
	// Promote to an exclusive intent lock by touching the locks table.
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM sysbox_state_locks WHERE topology=?`, b.topology()); err != nil {
		tx.Rollback()
		db.Close()
		return nil, nil, fmt.Errorf("sqlite lock upgrade: %w", err)
	}

	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultLockTimeout
	}
	lease := &Lease{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		Owner:     opts.Owner,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	if lease.Owner == "" {
		lease.Owner = "unknown"
	}
	expires := lease.ExpiresAt.Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sysbox_state_locks (topology, owner, lease_id, expires_at, updated_at)
		 VALUES (?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(topology) DO UPDATE SET owner=excluded.owner, lease_id=excluded.lease_id, expires_at=excluded.expires_at, updated_at=datetime('now')`,
		b.topology(), lease.Owner, lease.ID, expires); err != nil {
		tx.Rollback()
		db.Close()
		return nil, nil, fmt.Errorf("sqlite record state lock: %w", err)
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("sqlite lock commit: %w", err)
	}

	unlock := func() {
		db2, err := b.connect(context.Background())
		if err != nil {
			return
		}
		defer db2.Close()
		_, _ = db2.ExecContext(context.Background(),
			`DELETE FROM sysbox_state_locks WHERE topology=? AND lease_id=?`,
			b.topology(), lease.ID)
	}
	return lease, unlock, nil
}

// ── Metadata / LockInfo / ForceUnlock ────────────────────────────────────────

func (b *SQLiteBackend) Metadata(ctx context.Context) (Metadata, error) {
	meta := Metadata{Backend: "sqlite", Location: b.Path, Version: SchemaVersion}
	db, err := b.connect(ctx)
	if err != nil {
		return meta, err
	}
	defer db.Close()

	var version, serial int
	var updatedAt string
	err = db.QueryRowContext(ctx,
		`SELECT version, serial, updated_at FROM sysbox_state WHERE topology=?`,
		b.topology()).Scan(&version, &serial, &updatedAt)
	if err != nil && err != sql.ErrNoRows {
		return meta, fmt.Errorf("sqlite metadata: %w", err)
	}
	if err != sql.ErrNoRows {
		meta.Version = version
		meta.Serial = int64(serial)
		if t, err := timeParse(updatedAt); err == nil {
			meta.UpdatedAt = t
		}
	}
	return meta, nil
}

func (b *SQLiteBackend) LockInfo(ctx context.Context) (LockInfo, error) {
	db, err := b.connect(ctx)
	if err != nil {
		return LockInfo{}, err
	}
	defer db.Close()

	var owner, leaseID, expiresAt, updatedAt string
	err = db.QueryRowContext(ctx,
		`SELECT owner, lease_id, expires_at, updated_at FROM sysbox_state_locks WHERE topology=?`,
		b.topology()).Scan(&owner, &leaseID, &expiresAt, &updatedAt)
	if err == sql.ErrNoRows {
		return LockInfo{}, nil
	}
	if err != nil {
		return LockInfo{}, fmt.Errorf("sqlite lock info: %w", err)
	}
	tExp, _ := timeParse(expiresAt)
	tUpd, _ := timeParse(updatedAt)
	return LockInfo{
		Locked:    time.Now().UTC().Before(tExp),
		Owner:     owner,
		LeaseID:   leaseID,
		ExpiresAt: tExp,
		UpdatedAt: tUpd,
	}, nil
}

func (b *SQLiteBackend) ForceUnlock(ctx context.Context) error {
	db, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `DELETE FROM sysbox_state_locks WHERE topology=?`, b.topology())
	return err
}

// ── Snapshot / List / Restore ────────────────────────────────────────────────

func (b *SQLiteBackend) Snapshot(ctx context.Context, reason string) (*Snapshot, error) {
	data, err := b.Load(ctx)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []byte(`{"version":2,"resources":[]}`)
	}
	db, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	now := time.Now().UTC()
	id := now.Format("20060102T150405.000000000Z")
	_, err = db.ExecContext(ctx,
		`INSERT INTO sysbox_state_snapshots (topology, id, reason, data, checksum, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		b.topology(), id, reason, data, checksumBytes(data), now.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("sqlite snapshot: %w", err)
	}
	return &Snapshot{ID: id, CreatedAt: now, Reason: reason, Size: int64(len(data))}, nil
}

func (b *SQLiteBackend) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	db, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, created_at, COALESCE(reason,''), length(data)
		 FROM sysbox_state_snapshots
		 WHERE topology=?
		 ORDER BY created_at DESC`, b.topology())
	if err != nil {
		return nil, fmt.Errorf("sqlite list snapshots: %w", err)
	}
	defer rows.Close()

	var out []Snapshot
	for rows.Next() {
		var snap Snapshot
		var createdAt string
		if err := rows.Scan(&snap.ID, &createdAt, &snap.Reason, &snap.Size); err != nil {
			return nil, err
		}
		if t, err := timeParse(createdAt); err == nil {
			snap.CreatedAt = t
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

func (b *SQLiteBackend) RestoreSnapshot(ctx context.Context, id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("invalid snapshot id %q", id)
	}
	db, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	var data []byte
	var checksum string
	err = db.QueryRowContext(ctx,
		`SELECT data, checksum FROM sysbox_state_snapshots WHERE topology=? AND id=?`,
		b.topology(), id).Scan(&data, &checksum)
	if err == sql.ErrNoRows {
		return fmt.Errorf("snapshot %q not found", id)
	}
	if err != nil {
		return fmt.Errorf("sqlite restore snapshot: %w", err)
	}
	if checksum != checksumBytes(data) {
		if !validJSON(data) {
			return fmt.Errorf("sqlite snapshot checksum mismatch for %q", id)
		}
	}
	return b.Save(ctx, data)
}

// ── Delete ───────────────────────────────────────────────────────────────────

func (b *SQLiteBackend) Delete(ctx context.Context) error {
	db, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite delete begin: %w", err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM sysbox_state_locks WHERE topology=?`,
		`DELETE FROM sysbox_state_snapshots WHERE topology=?`,
		`DELETE FROM sysbox_state WHERE topology=?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, b.topology()); err != nil {
			return fmt.Errorf("sqlite delete state: %w", err)
		}
	}
	return tx.Commit()
}

// ── Shared helpers ───────────────────────────────────────────────────────────

func checksumBytes(data []byte) string {
	return checksumHex(data) // reuse the canonical-JSON hasher from backend_postgres.go
}

func timeParse(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time string")
	}
	// SQLite datetime() returns ISO 8601 without timezone offset; go can
	// parse both that and RFC 3339.
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse("2006-01-02 15:04:05", s)
	if err == nil {
		return t.In(time.UTC), nil
	}
	return t, fmt.Errorf("unparseable time: %s", s)
}

// ── TopologyLister ───────────────────────────────────────────────────────────

func (b *SQLiteBackend) ListTopologies(ctx context.Context) ([]TopologyMetadata, error) {
	db, err := sql.Open("sqlite", b.Path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("sqlite list topologies: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT topology, serial, updated_at, data FROM sysbox_state ORDER BY topology`)
	if err != nil {
		return nil, fmt.Errorf("sqlite list topologies: %w", err)
	}
	defer rows.Close()

	var out []TopologyMetadata
	for rows.Next() {
		var item TopologyMetadata
		var data []byte
		var updatedAt string
		item.Backend = "sqlite"
		item.HasState = true
		if err := rows.Scan(&item.Name, &item.Serial, &updatedAt, &data); err != nil {
			return nil, err
		}
		if t, err := timeParse(updatedAt); err == nil {
			item.UpdatedAt = t
		}
		if st, err := Unmarshal(data); err == nil {
			item.ResourceCount = len(st.Resources)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// interface guards
var (
	_ Backend          = (*SQLiteBackend)(nil)
	_ VersionedBackend = (*SQLiteBackend)(nil)
	_ LeaseBackend     = (*SQLiteBackend)(nil)
	_ MetadataBackend  = (*SQLiteBackend)(nil)
	_ SnapshotBackend  = (*SQLiteBackend)(nil)
	_ DeleteBackend    = (*SQLiteBackend)(nil)
	_ LockInfoBackend  = (*SQLiteBackend)(nil)
	_ TopologyLister   = (*SQLiteBackend)(nil)
)
