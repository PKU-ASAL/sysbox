package controlstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func commitSQLiteFencedAdoption(ctx context.Context, store *SQLite, topology string, external *persistence.Repository, apply func([]byte, []byte) ([]byte, error)) error {

	db, err := store.OpenDB()
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
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
	var authority, raw []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&authority); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if external != nil {
		if err = external.ApplyAdoptionWithAuthority(ctx, topology, authority, apply); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", topology).Scan(&raw); err != nil {
		return err
	}
	next, err := apply(raw, authority)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, next) {
		if _, err = conn.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?", next, topology); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func commitPostgresFencedAdoption(ctx context.Context, store *Postgres, topology string, external *persistence.Repository, apply func([]byte, []byte) ([]byte, error)) error {

	pool, err := store.PoolFor(ctx)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// Match recovery/admission ordering. Authority is always an existing row:
	// a missing ledger fails, so concurrent initialization cannot bypass freeze.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return err
	}
	var authority, raw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if external != nil {
		if err = external.ApplyAdoptionWithAuthority(ctx, topology, authority, apply); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", topology).Scan(&raw); err != nil {
		return err
	}
	next, err := apply(raw, authority)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, next) {
		if _, err = tx.Exec(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2", string(next), topology); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Transactions) CommitAdoptionWithAuthority(ctx context.Context, topology string, apply func([]byte, []byte) ([]byte, error)) error {
	repo, err := s.ActionRepository(topology)
	if err != nil {
		return err
	}
	if _, err = repo.LoadState(ctx, topology); err != nil {
		return err
	}
	switch store := s.Store.(type) {
	case *Local:
		unlock, err := store.LockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		return persistence.FilesystemExecutionGrantStorage(store.RunsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
			repo, err := s.ActionRepository(topology)
			if err != nil {
				return nil, err
			}
			return nil, repo.ApplyAdoptionWithAuthority(ctx, topology, authority, apply)
		})
	case *SQLite:
		var external *persistence.Repository
		resolved, _, err := persistence.ResolveLocation(s.StateBackend, "", topology)
		if err != nil {
			return err
		}
		if strings.HasPrefix(resolved, "sqlite:") {
			path, err := persistence.SQLitePath(resolved)
			if err != nil {
				return err
			}
			left, err := os.Stat(path)
			if err != nil {
				return err
			}
			right, err := os.Stat(store.DbPath)
			if err != nil {
				return err
			}
			if !os.SameFile(left, right) {
				external = repo
			}
		} else {
			external = repo
		}
		return commitSQLiteFencedAdoption(ctx, store, topology, external, apply)
	case *Postgres:
		var external *persistence.Repository
		resolved, _, err := persistence.ResolveLocation(s.StateBackend, "", topology)
		if err != nil {
			return err
		}
		if strings.HasPrefix(resolved, "postgres") {
			left, err := pgx.ParseConfig(DsnWithoutSysboxQuery(resolved))
			if err != nil {
				return err
			}
			right, err := pgx.ParseConfig(DsnWithoutSysboxQuery(store.DSN))
			if err != nil {
				return err
			}
			if left.Host != right.Host || left.Port != right.Port || left.Database != right.Database || left.RuntimeParams["search_path"] != right.RuntimeParams["search_path"] {
				return fmt.Errorf("adoption recovery requires the canonical PostgreSQL catalog")
			}
		} else {
			external = repo
		}
		return commitPostgresFencedAdoption(ctx, store, topology, external, apply)
	default:
		return persistence.ErrUnsupportedVersion
	}
}

func (s *Transactions) CommitFencedAdoption(ctx context.Context, topology string, p controlplane.AdoptionDispositionProof, host, issuer ed25519.PublicKey) error {
	return s.CommitAdoptionWithAuthority(ctx, topology, func(raw, authority []byte) ([]byte, error) {
		return persistence.ApplyFencedAdoption(raw, authority, p, host, issuer)
	})
}
