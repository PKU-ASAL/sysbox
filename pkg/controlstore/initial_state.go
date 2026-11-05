package controlstore

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// InitializeFirstApply holds the existing admission/authority/lifecycle locks
// through the initial empty-state commit. It never creates a new topology UID.
func (s *Transactions) InitializeFirstApply(ctx context.Context, run controlplane.Run, grant controlplane.ExecutionGrant) error {
	if err := ValidatePathSegment(run.Topology, "topology"); err != nil {
		return err
	}
	switch store := s.Store.(type) {
	case *Local:
		unlock, err := store.LockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		repo, err := s.ActionRepository(run.Topology)
		if err != nil {
			return err
		}
		return persistence.FilesystemExecutionGrantStorage(store.RunsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
			runs, err := store.LoadRuns(ctx)
			if err != nil {
				return nil, err
			}
			next, err := validateInitialApply(ctx, authority, runs, run, grant)
			if err != nil {
				return nil, err
			}
			if err = repo.InitializeAuthorizedTopology(ctx, run.Topology, grant.Scope.TopologyUID); err != nil {
				return nil, err
			}
			return next, nil
		})
	case *SQLite:
		return initializeSQLiteApply(ctx, store, run, grant)
	case *Postgres:
		return initializePostgresApply(ctx, store, run, grant)
	default:
		return persistence.ErrUnsupportedVersion
	}
}

func validateInitialApply(ctx context.Context, authority []byte, history []controlplane.Run, run controlplane.Run, grant controlplane.ExecutionGrant) ([]byte, error) {
	if run.Op != "apply" || run.ID == "" || run.AgentID == "" || run.QueuedAt.IsZero() || run.Revision == "" || grant.Operation != "run_apply" || grant.ActionID != run.ID || grant.AgentID != run.AgentID || grant.Scope.TopologyUID == "" || grant.Scope.ResourceUID != "" || grant.Scope.Generation != 0 {
		return nil, persistence.ErrInvalidRecord
	}
	for _, prior := range history {
		if prior.Topology == run.Topology && !controlplane.SameRunIncarnation(prior, run) {
			return nil, fmt.Errorf("%w: missing initial state with existing execution history", persistence.ErrConflict)
		}
	}
	found := false
	for _, current := range LatestRunsByID(history) {
		if current.ID != run.ID {
			continue
		}
		if !controlplane.SameRunIncarnation(current, run) || current.Status != controlplane.RunRunning || current.AgentID != run.AgentID || current.Attempt != run.Attempt || current.LeaseOwner != run.LeaseOwner || !current.LeaseUntil.After(time.Now()) {
			return nil, persistence.ErrConflict
		}
		found = true
	}
	if !found {
		return nil, persistence.ErrNotFound
	}
	if err := persistence.ValidateMaintenanceWrite(authority, grant.Scope.TopologyUID, grant.AgentID); err != nil {
		return nil, err
	}
	memory := &GrantTransaction{Raw: authority}
	if err := persistence.NewExecutionGrantRepository(memory).Authorize(ctx, grant); err != nil {
		return nil, err
	}
	return memory.Raw, nil
}

func initializeSQLiteApply(ctx context.Context, store *SQLite, run controlplane.Run, grant controlplane.ExecutionGrant) error {
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
			if _, e := conn.ExecContext(cleanup, "ROLLBACK"); e != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	var authority, raw []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&authority); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, "SELECT "+SqliteRunColumns+" FROM sysbox_runs WHERE topology=? ORDER BY id,rowid", run.Topology)
	if err != nil {
		return err
	}
	var runs []controlplane.Run
	for rows.Next() {
		r, e := ScanSQLiteRun(rows)
		if e != nil {
			rows.Close()
			return e
		}
		runs = append(runs, *r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	nextAuthority, err := validateInitialApply(ctx, authority, runs, run, grant)
	if err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", run.Topology).Scan(&raw); err != nil {
		return err
	}
	next, err := persistence.ApplyInitialTopology(raw, run.Topology, grant.Scope.TopologyUID)
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?", next, run.Topology); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_execution_authority SET data=? WHERE id='authority'", nextAuthority); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func initializePostgresApply(ctx context.Context, store *Postgres, run controlplane.Run, grant controlplane.ExecutionGrant) error {
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
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return err
	}
	authority, err := lockPostgresExecutionAuthority(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT data::text FROM sysbox_runs WHERE topology=$1 FOR UPDATE", run.Topology)
	if err != nil {
		return err
	}
	var runs []controlplane.Run
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var r controlplane.Run
		if err = json.Unmarshal(raw, &r); err != nil {
			rows.Close()
			return err
		}
		controlplane.NormalizeRun(&r)
		runs = append(runs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	nextAuthority, err := validateInitialApply(ctx, authority, runs, run, grant)
	if err != nil {
		return err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", run.Topology).Scan(&raw); err != nil {
		return err
	}
	next, err := persistence.ApplyInitialTopology(raw, run.Topology, grant.Scope.TopologyUID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2", string(next), run.Topology); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb,updated_at=now() WHERE id='authority'", string(nextAuthority)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
