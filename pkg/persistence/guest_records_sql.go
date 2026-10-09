package persistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Both constructors borrow a pool with the API metadata tables already created.
// Table names come only from the private guestKind constants, never user input.
func NewSQLiteGuestRepository(db *sql.DB) *GuestRepository {
	return &GuestRepository{records: &sqliteGuestRecords{db: db}}
}
func NewPostgresGuestRepository(pool *pgxpool.Pool) *GuestRepository {
	return &GuestRepository{records: &postgresGuestRecords{pool: pool}}
}

type sqliteGuestRecords struct{ db *sql.DB }
type postgresGuestRecords struct{ pool *pgxpool.Pool }

func (b *sqliteGuestRecords) get(ctx context.Context, kind guestKind, id string) ([]byte, error) {
	var raw []byte
	err := b.db.QueryRowContext(ctx, "SELECT data FROM "+string(kind)+" WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, err
}
func (b *sqliteGuestRecords) list(ctx context.Context, kind guestKind) ([]guestRawRecord, error) {
	rows, err := b.db.QueryContext(ctx, "SELECT id,data FROM "+string(kind)+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []guestRawRecord
	for rows.Next() {
		var row guestRawRecord
		if err := rows.Scan(&row.id, &row.data); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
func (b *sqliteGuestRecords) update(ctx context.Context, kind guestKind, id string, fn func([]byte) ([]byte, error)) error {
	conn, err := b.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	var raw []byte
	err = conn.QueryRowContext(ctx, "SELECT data FROM "+string(kind)+" WHERE id=?", id).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		raw = nil
	}
	next, err := fn(raw)
	if err != nil {
		return err
	}
	if next != nil {
		if isOperationRecord(kind) {
			if err := checkSQLiteOperationMaintenance(ctx, conn, kind, raw, next); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO "+string(kind)+" (id,data) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, next)
		if err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
func (b *postgresGuestRecords) get(ctx context.Context, kind guestKind, id string) ([]byte, error) {
	var raw []byte
	err := b.pool.QueryRow(ctx, "SELECT data::text FROM "+string(kind)+" WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, err
}
func (b *postgresGuestRecords) list(ctx context.Context, kind guestKind) ([]guestRawRecord, error) {
	rows, err := b.pool.Query(ctx, "SELECT id,data::text FROM "+string(kind)+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []guestRawRecord
	for rows.Next() {
		var row guestRawRecord
		if err := rows.Scan(&row.id, &row.data); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
func (b *postgresGuestRecords) update(ctx context.Context, kind guestKind, id string, fn func([]byte) ([]byte, error)) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var authority []byte
	if isOperationRecord(kind) {
		authority, err = lockOperationAuthority(ctx, tx)
		if err != nil {
			return err
		}
	}
	// Inserting a private transaction-local placeholder locks even a missing
	// key. Concurrent creators wait and then validate the winner's full record.
	tag, err := tx.Exec(ctx, "INSERT INTO "+string(kind)+" (id,data) VALUES ($1,'{}'::jsonb) ON CONFLICT(id) DO NOTHING", id)
	if err != nil {
		return err
	}
	var raw []byte
	if tag.RowsAffected() == 0 {
		err = tx.QueryRow(ctx, "SELECT data::text FROM "+string(kind)+" WHERE id=$1 FOR UPDATE", id).Scan(&raw)
		if err != nil {
			return err
		}
	}
	next, err := fn(raw)
	if err != nil {
		return err
	}
	if next != nil {
		if isOperationRecord(kind) {
			if err := checkPostgresOperationMaintenance(ctx, tx, kind, authority, raw, next); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE "+string(kind)+" SET data=$1::jsonb,updated_at=now() WHERE id=$2", string(next), id)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
