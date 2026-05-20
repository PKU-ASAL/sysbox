package state

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const postgresDefaultTopology = "default"
const postgresSchemaVersion = 2

func (b *PostgresBackend) topology() string {
	if b.Topology != "" {
		return b.Topology
	}
	return postgresDefaultTopology
}

func (b *PostgresBackend) dsnWithoutSysboxQuery() string {
	u, err := url.Parse(b.DSN)
	if err != nil {
		return b.DSN
	}
	q := u.Query()
	q.Del("topology")
	u.RawQuery = q.Encode()
	return u.String()
}

func (b *PostgresBackend) connect(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, b.dsnWithoutSysboxQuery())
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	if err := b.ensureSchema(ctx, conn); err != nil {
		conn.Close(ctx)
		return nil, err
	}
	return conn, nil
}

func (b *PostgresBackend) ensureSchema(ctx context.Context, conn *pgx.Conn) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres migration begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, postgresAdvisoryKey("sysbox:state:migrate")); err != nil {
		return fmt.Errorf("postgres migration lock: %w", err)
	}

	if _, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS sysbox_schema_migrations (
  component TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`); err != nil {
		return fmt.Errorf("postgres ensure migration table: %w", err)
	}

	var current int
	err = tx.QueryRow(ctx, `SELECT version FROM sysbox_schema_migrations WHERE component='state'`).Scan(&current)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("postgres read migration version: %w", err)
	}
	if current > postgresSchemaVersion {
		return fmt.Errorf("postgres state schema v%d is newer than supported v%d", current, postgresSchemaVersion)
	}
	if current < 1 {
		if _, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS sysbox_state (
  topology TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  serial BIGINT NOT NULL DEFAULT 0,
  data JSONB NOT NULL,
  checksum TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_state_snapshots (
  topology TEXT NOT NULL,
  id TEXT NOT NULL,
  reason TEXT,
  data JSONB NOT NULL,
  checksum TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (topology, id)
);`); err != nil {
			return fmt.Errorf("postgres migrate state schema v1: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO sysbox_schema_migrations (component, version, updated_at)
VALUES ('state', 1, now())
ON CONFLICT (component) DO UPDATE SET version=1, updated_at=now()`); err != nil {
			return fmt.Errorf("postgres record migration v1: %w", err)
		}
		current = 1
	}
	if current < 2 {
		if _, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS sysbox_state_locks (
  topology TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  lease_id TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`); err != nil {
			return fmt.Errorf("postgres migrate state schema v2: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO sysbox_schema_migrations (component, version, updated_at)
VALUES ('state', 2, now())
ON CONFLICT (component) DO UPDATE SET version=2, updated_at=now()`); err != nil {
			return fmt.Errorf("postgres record migration v2: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (b *PostgresBackend) Load(ctx context.Context) ([]byte, error) {
	conn, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)

	var data []byte
	var checksum string
	err = conn.QueryRow(ctx, `SELECT data::text, checksum FROM sysbox_state WHERE topology=$1`, b.topology()).Scan(&data, &checksum)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres load state: %w", err)
	}
	if checksum != checksumHex(data) {
		if !validJSON(data) {
			return nil, fmt.Errorf("postgres state checksum mismatch for topology %q", b.topology())
		}
	}
	return data, nil
}

func (b *PostgresBackend) Save(ctx context.Context, data []byte) error {
	conn, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_state (topology, version, serial, data, checksum, updated_at)
VALUES ($1, $2, 1, $3::jsonb, $4, now())
ON CONFLICT (topology) DO UPDATE SET
  version = EXCLUDED.version,
  serial = sysbox_state.serial + 1,
  data = EXCLUDED.data,
  checksum = EXCLUDED.checksum,
  updated_at = now()`,
		b.topology(), SchemaVersion, string(data), checksumHex(data))
	if err != nil {
		return fmt.Errorf("postgres save state: %w", err)
	}
	return nil
}

func (b *PostgresBackend) Lock(ctx context.Context) (UnlockFunc, error) {
	_, unlock, err := b.LockWithOptions(ctx, LockOptions{})
	return unlock, err
}

func (b *PostgresBackend) LockWithOptions(ctx context.Context, opts LockOptions) (*Lease, UnlockFunc, error) {
	conn, err := b.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	key := postgresAdvisoryKey("sysbox:" + b.topology())
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		conn.Close(ctx)
		return nil, nil, fmt.Errorf("postgres advisory lock: %w", err)
	}
	if !locked {
		conn.Close(ctx)
		return nil, nil, fmt.Errorf("state is locked by another process")
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
	if _, err := conn.Exec(ctx, `
INSERT INTO sysbox_state_locks (topology, owner, lease_id, expires_at, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (topology) DO UPDATE SET
  owner=EXCLUDED.owner,
  lease_id=EXCLUDED.lease_id,
  expires_at=EXCLUDED.expires_at,
  updated_at=now()`,
		b.topology(), lease.Owner, lease.ID, lease.ExpiresAt); err != nil {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
		_ = conn.Close(context.Background())
		return nil, nil, fmt.Errorf("postgres record state lock: %w", err)
	}
	unlock := func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM sysbox_state_locks WHERE topology=$1 AND lease_id=$2`, b.topology(), lease.ID)
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
		_ = conn.Close(context.Background())
	}
	return lease, unlock, nil
}

func (b *PostgresBackend) Metadata(ctx context.Context) (Metadata, error) {
	meta := Metadata{Backend: "postgres", Location: b.redactedLocation(), Version: SchemaVersion}
	conn, err := b.connect(ctx)
	if err != nil {
		return meta, err
	}
	defer conn.Close(ctx)

	err = conn.QueryRow(ctx, `SELECT version, serial, updated_at FROM sysbox_state WHERE topology=$1`, b.topology()).
		Scan(&meta.Version, &meta.Serial, &meta.UpdatedAt)
	if err != nil && err != pgx.ErrNoRows {
		return meta, fmt.Errorf("postgres metadata: %w", err)
	}
	return meta, nil
}

func (b *PostgresBackend) LockInfo(ctx context.Context) (LockInfo, error) {
	conn, err := b.connect(ctx)
	if err != nil {
		return LockInfo{}, err
	}
	defer conn.Close(ctx)

	var info LockInfo
	err = conn.QueryRow(ctx, `
SELECT owner, lease_id, expires_at, updated_at
FROM sysbox_state_locks
WHERE topology=$1`, b.topology()).Scan(&info.Owner, &info.LeaseID, &info.ExpiresAt, &info.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return LockInfo{}, nil
		}
		return LockInfo{}, fmt.Errorf("postgres lock info: %w", err)
	}
	info.Locked = time.Now().UTC().Before(info.ExpiresAt)
	return info, nil
}

func (b *PostgresBackend) ForceUnlock(ctx context.Context) error {
	conn, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `DELETE FROM sysbox_state_locks WHERE topology=$1`, b.topology())
	if err != nil {
		return fmt.Errorf("postgres force unlock: %w", err)
	}
	return nil
}

func (b *PostgresBackend) Snapshot(ctx context.Context, reason string) (*Snapshot, error) {
	data, err := b.Load(ctx)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []byte(`{"version":2,"resources":[]}`)
	}
	conn, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)

	now := time.Now().UTC()
	id := now.Format("20060102T150405.000000000Z")
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_state_snapshots (topology, id, reason, data, checksum, created_at)
VALUES ($1, $2, $3, $4::jsonb, $5, $6)`,
		b.topology(), id, reason, string(data), checksumHex(data), now)
	if err != nil {
		return nil, fmt.Errorf("postgres snapshot: %w", err)
	}
	return &Snapshot{ID: id, CreatedAt: now, Reason: reason, Size: int64(len(data))}, nil
}

func (b *PostgresBackend) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	conn, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `
SELECT id, created_at, COALESCE(reason, ''), octet_length(data::text)
FROM sysbox_state_snapshots
WHERE topology=$1
ORDER BY created_at DESC`, b.topology())
	if err != nil {
		return nil, fmt.Errorf("postgres list snapshots: %w", err)
	}
	defer rows.Close()

	var out []Snapshot
	for rows.Next() {
		var snap Snapshot
		if err := rows.Scan(&snap.ID, &snap.CreatedAt, &snap.Reason, &snap.Size); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

func (b *PostgresBackend) RestoreSnapshot(ctx context.Context, id string) error {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return fmt.Errorf("invalid snapshot id %q", id)
	}
	conn, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var data []byte
	var checksum string
	err = conn.QueryRow(ctx, `
SELECT data::text, checksum
FROM sysbox_state_snapshots
WHERE topology=$1 AND id=$2`, b.topology(), id).Scan(&data, &checksum)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("snapshot %q not found", id)
		}
		return fmt.Errorf("postgres restore snapshot: %w", err)
	}
	if checksum != checksumHex(data) {
		if !validJSON(data) {
			return fmt.Errorf("postgres snapshot checksum mismatch for %q", id)
		}
	}
	return b.Save(ctx, data)
}

func (b *PostgresBackend) Delete(ctx context.Context) error {
	conn, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `
DELETE FROM sysbox_state_locks WHERE topology=$1;
DELETE FROM sysbox_state_snapshots WHERE topology=$1;
DELETE FROM sysbox_state WHERE topology=$1;`, b.topology())
	if err != nil {
		return fmt.Errorf("postgres delete state: %w", err)
	}
	return nil
}

func (b *PostgresBackend) redactedLocation() string {
	u, err := url.Parse(b.DSN)
	if err != nil {
		return "postgres"
	}
	if u.User != nil {
		username := u.User.Username()
		if username != "" {
			u.User = url.UserPassword(username, "xxxxx")
		}
	}
	return u.String()
}

func checksumHex(data []byte) string {
	canonical := data
	var v any
	if err := json.Unmarshal(data, &v); err == nil {
		if raw, err := json.Marshal(v); err == nil {
			canonical = raw
		}
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", sum[:])
}

func validJSON(data []byte) bool {
	var v any
	return json.Unmarshal(data, &v) == nil
}

func postgresAdvisoryKey(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(binary.BigEndian.Uint64(h.Sum(nil)))
}
