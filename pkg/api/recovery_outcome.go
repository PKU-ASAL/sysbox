package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
)

type recoveryActionRepository struct {
	runtime.ActionRepository
	server        *Server
	proof         hostsupervisor.RunRecoveryRequest
	run           controlplane.Run
	localRecovery bool
}

func (r *recoveryActionRepository) CommitActionIntent(context.Context, runtime.ActionRecord) error {
	return persistence.ErrConflict // Recovery never authorizes a new effect.
}

func (r *recoveryActionRepository) validate(record *persistence.CommandRecord, owner *controlplane.Agent, runs []controlplane.Run, action runtime.ActionRecord) error {
	if r.localRecovery {
		return persistence.ValidateLocalRecoveryCommit(r.run, latestRunsByID(runs), action, time.Now().UTC())
	}
	return persistence.ValidateRecoveryCommit(controlplane.AgentCommandEvent{CommandID: r.proof.CommandID, LeaseOwner: r.proof.LeaseOwner, Attempt: r.proof.Attempt}, r.run, record, owner, latestRunsByID(runs), action, time.Now().UTC())
}

func (r *recoveryActionRepository) CommitActionOutcome(ctx context.Context, action runtime.ActionRecord, patch *runtime.StatePatch) error {
	if err := r.proof.Validate(); err != nil && !r.localRecovery {
		return err
	}
	if action.Topology != r.run.Topology {
		return persistence.ErrConflict
	}
	switch store := r.server.apiStore.(type) {
	case *localAPIStore:
		unlock, err := store.lockRunRecords(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		var record *persistence.CommandRecord
		var owner *controlplane.Agent
		if !r.localRecovery {
			record, err = (localCommandStorage{store: store}).read(ctx, r.proof.CommandID)
			if err != nil {
				return err
			}
			owner, err = store.GetAgent(ctx, r.run.AgentID)
			if err != nil {
				return err
			}
		}
		runs, err := store.LoadRuns(ctx)
		if err != nil {
			return err
		}
		repo, ok := r.ActionRepository.(*persistence.Repository)
		if !ok {
			return persistence.ErrConflict
		}
		return persistence.FilesystemExecutionGrantStorage(store.runsDir).UpdateExecutionAuthority(ctx, func(authority []byte) ([]byte, error) {
			if err := persistence.ValidateMaintenanceWrite(authority, action.OwnerUID); err != nil {
				return nil, err
			}
			err := repo.CommitActionOutcomeWithGuard(ctx, action, patch, func() error { return r.validate(record, owner, runs, action) })
			return nil, err // Lock authority without rewriting it.
		})
	case *sqliteAPIStore:
		return r.commitSQLite(ctx, store, action, patch)
	case *postgresAPIStore:
		return r.commitPostgres(ctx, store, action, patch)
	default:
		return persistence.ErrUnsupportedVersion
	}
}

func (r *recoveryActionRepository) commitSQLite(ctx context.Context, store *sqliteAPIStore, action runtime.ActionRecord, patch *runtime.StatePatch) error {
	db, err := store.open()
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
	var authority []byte
	err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&authority)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && len(authority) == 0 {
		return persistence.ErrCorruptRecord
	}
	if err = persistence.ValidateMaintenanceWrite(authority, action.OwnerUID); err != nil {
		return err
	}
	var record *persistence.CommandRecord
	var owner *controlplane.Agent
	if !r.localRecovery {
		record, err = scanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+sqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", r.proof.CommandID))
		if err != nil {
			return err
		}
		owner, err = scanSQLiteAgent(conn.QueryRowContext(ctx, "SELECT "+sqliteAgentColumns+" FROM sysbox_agents WHERE id=?", r.run.AgentID))
		if err != nil {
			return err
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT "+sqliteRunColumns+" FROM sysbox_runs WHERE topology=? ORDER BY id,rowid", r.run.Topology)
	if err != nil {
		return err
	}
	var runs []controlplane.Run
	for rows.Next() {
		run, err := scanSQLiteRun(rows)
		if err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, *run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var raw []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", action.Topology).Scan(&raw); err != nil {
		return err
	}
	if err = r.validate(record, owner, runs, action); err != nil {
		return err
	}
	next, err := persistence.ApplyActionOutcome(raw, action, patch)
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?", next, action.Topology); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *recoveryActionRepository) commitPostgres(ctx context.Context, store *postgresAPIStore, action runtime.ActionRecord, patch *runtime.StatePatch) error {
	// Ensure the ledger row exists before taking a row lock; an absent-row
	// SELECT cannot serialize with a concurrently created maintenance barrier.
	if err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).ObserveTime(ctx); err != nil {
		return err
	}
	pool, err := store.poolFor(ctx)
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
	// Match Run admission/update lock ordering; prevent a newly admitted Run
	// from appearing between the inactive-run check and aggregate commit.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return err
	}
	var authority []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&authority); err != nil {
		return err
	}
	if err = persistence.ValidateMaintenanceWrite(authority, action.OwnerUID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT data::text FROM sysbox_runs WHERE topology=$1 FOR UPDATE", r.run.Topology)
	if err != nil {
		return err
	}
	var runs []controlplane.Run
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var run controlplane.Run
		if err := json.Unmarshal(raw, &run); err != nil {
			rows.Close()
			return err
		}
		persistence.NormalizeRun(&run)
		runs = append(runs, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var record *persistence.CommandRecord
	var owner *controlplane.Agent
	if !r.localRecovery {
		record, err = scanPostgresCommand(tx.QueryRow(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", r.proof.CommandID))
		if err != nil {
			return err
		}
		owner, err = scanPostgresAgent(tx.QueryRow(ctx, "SELECT "+postgresAgentColumns+" FROM sysbox_agents WHERE id=$1 FOR SHARE", r.run.AgentID))
		if err != nil {
			return err
		}
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", action.Topology).Scan(&raw); err != nil {
		return err
	}
	if err = r.validate(record, owner, runs, action); err != nil {
		return err
	}
	next, err := persistence.ApplyActionOutcome(raw, action, patch)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2", string(next), action.Topology); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (b *HostExecutionBridge) RecoveryActionRepository(ctx context.Context, proof hostsupervisor.RunRecoveryRequest, run controlplane.Run) (runtime.ActionRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, err := b.server.actionRepository(run.Topology)
	if err != nil {
		return nil, err
	}
	return &recoveryActionRepository{ActionRepository: repo, server: b.server, proof: proof, run: run}, nil
}
