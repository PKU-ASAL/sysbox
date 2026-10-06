package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

var errIdempotencyConflict = errors.New("idempotency key was already used for a different request")

type localRunDispatch struct {
	// Existing runs only acquire an idempotency mapping, not another command.
	RequestOnly bool                      `json:"request_only,omitempty"`
	Fingerprint string                    `json:"fingerprint"`
	Run         controlplane.Run          `json:"run"`
	Command     controlplane.AgentCommand `json:"command"`
}

func (s *localAPIStore) GetRunDispatch(_ context.Context, requestID, fingerprint string) (*controlplane.Run, bool, error) {
	bundle, err := readLocalRunDispatch(filepath.Join(s.runsDir, "_run_requests", requestID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if fingerprint != "" && bundle.Fingerprint != fingerprint {
		return nil, false, errIdempotencyConflict
	}
	return &bundle.Run, true, nil
}

func (s *localAPIStore) CreateRunDispatch(ctx context.Context, request RunDispatchRequest) (*controlplane.Run, bool, error) {
	if err := validatePathSegment(request.Run.ID, "run"); err != nil {
		return nil, false, err
	}
	unlock, err := s.lockRunRecords(ctx)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	request = durableRunDispatch(request)
	target := filepath.Join(s.runsDir, "_run_requests", request.Run.ID+".json")
	// Check the durable request mapping before looking at mutable run history.
	// Its fingerprint remains authoritative even when old Run fields are empty.
	previous, err := readLocalRunDispatch(target)
	if err == nil {
		if previous.Fingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		current, err := s.GetRun(ctx, request.Run.ID)
		return current, false, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	bundle := localRunDispatch{Fingerprint: request.Fingerprint, Run: request.Run, Command: request.Command}
	existing, err := s.GetRun(ctx, request.Run.ID)
	created := errors.Is(err, persistence.ErrNotFound)
	if err != nil && !created {
		return nil, false, err
	}
	if !created {
		if existing.RequestFingerprint != "" && existing.RequestFingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		if strings.HasPrefix(request.Fingerprint, "run-dispatch:") && request.Command.ID != "" {
			assigned, err := persistence.AssignRun(*existing, request.Run, request.Fingerprint)
			if err != nil {
				return nil, false, err
			}
			request.Run = assigned
		}
		if strings.HasPrefix(request.Fingerprint, "run-dispatch:") && request.Command.ID != "" && existing.Status == controlplane.RunQueued {
			bundle.Run = request.Run
			bundle.RequestOnly = false
		} else {
			bundle.Run = *existing
		}
		if request.Fingerprint == "" || request.Command.ID == "" || !strings.HasPrefix(request.Fingerprint, "run-dispatch:") || bundle.RequestOnly {
			bundle.RequestOnly = true
			bundle.Command = controlplane.AgentCommand{}
		} else {
			if current, readErr := (localCommandStorage{store: s}).read(ctx, request.Command.ID); readErr != nil {
				return nil, false, readErr
			} else if current != nil {
				return nil, false, persistence.ErrConflict
			}
			bundle.Run.RequestFingerprint = request.Fingerprint
		}
	} else {
		if err := validatePathSegment(request.Command.ID, "command"); err != nil {
			return nil, false, err
		}
		current, err := (localCommandStorage{store: s}).read(ctx, request.Command.ID)
		if err != nil {
			return nil, false, err
		}
		if current != nil {
			return nil, false, persistence.ErrConflict
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	// All participating writers hold the stable record lock; rename plus file
	// and directory fsync publishes either the complete bundle or nothing.
	if err := writeLocalObject(target, bundle); err != nil {
		return nil, false, err
	}
	return &bundle.Run, created, nil
}

func readLocalRunDispatch(path string) (*localRunDispatch, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bundle localRunDispatch
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, err
	}
	return &bundle, nil
}

func (s *localAPIStore) loadRunDispatches() ([]localRunDispatch, error) {
	files, err := localRecordPaths(filepath.Join(s.runsDir, "_run_requests", "*.json"))
	if err != nil {
		return nil, err
	}
	out := make([]localRunDispatch, 0, len(files))
	for _, file := range files {
		bundle, err := readLocalRunDispatch(file)
		if err != nil {
			return nil, err
		}
		out = append(out, *bundle)
	}
	return out, nil
}

func (s *sqliteAPIStore) CreateRunDispatch(ctx context.Context, request RunDispatchRequest) (*controlplane.Run, bool, error) {
	request = durableRunDispatch(request)
	db, err := s.open()
	if err != nil {
		return nil, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO sysbox_run_requests (id, fingerprint, run_id) VALUES (?, ?, ?)`, request.Run.ID, request.Fingerprint, request.Run.ID)
	if err != nil {
		return nil, false, err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if created == 0 {
		var fingerprint, runID string
		if err := tx.QueryRowContext(ctx, `SELECT fingerprint, run_id FROM sysbox_run_requests WHERE id=?`, request.Run.ID).Scan(&fingerprint, &runID); err != nil {
			return nil, false, err
		}
		if fingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		run, err := s.GetRun(ctx, runID)
		return run, false, err
	}
	existing, err := scanSQLiteRun(tx.QueryRowContext(ctx, "SELECT "+sqliteRunColumns+" FROM sysbox_runs WHERE id=? ORDER BY rowid DESC LIMIT 1", request.Run.ID))
	if err == nil {
		existingFingerprint := existing.RequestFingerprint
		if existingFingerprint != "" && existingFingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		if strings.HasPrefix(request.Fingerprint, "run-dispatch:") && request.Command.ID != "" {
			assigned, err := persistence.AssignRun(*existing, request.Run, request.Fingerprint)
			if err != nil {
				return nil, false, err
			}
			request.Run = assigned
			if err := insertSQLiteRun(ctx, tx, request.Run); err != nil {
				return nil, false, err
			}
		}
		if strings.HasPrefix(request.Fingerprint, "run-dispatch:") && request.Command.ID != "" {
			if err := insertSQLiteAgentCommand(ctx, tx, request.Command); err != nil {
				return nil, false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		run, err := s.GetRun(ctx, request.Run.ID)
		return run, false, err
	}
	if !errors.Is(err, persistence.ErrNotFound) {
		return nil, false, err
	}
	if err := insertSQLiteRun(ctx, tx, request.Run); err != nil {
		return nil, false, err
	}
	if err := insertSQLiteAgentCommand(ctx, tx, request.Command); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	run := request.Run
	return &run, true, nil
}

func (s *sqliteAPIStore) GetRunDispatch(ctx context.Context, requestID, fingerprint string) (*controlplane.Run, bool, error) {
	db, err := s.open()
	if err != nil {
		return nil, false, err
	}
	var storedFingerprint, runID string
	err = db.QueryRowContext(ctx, `SELECT fingerprint, run_id FROM sysbox_run_requests WHERE id=?`, requestID).Scan(&storedFingerprint, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if fingerprint != "" && storedFingerprint != fingerprint {
		return nil, false, errIdempotencyConflict
	}
	run, err := s.GetRun(ctx, runID)
	return run, err == nil, err
}

type sqliteRunWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertSQLiteRun(ctx context.Context, tx sqliteRunWriter, run controlplane.Run) error {
	run = persistence.DurableRun(run)
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	recoverable, unsafeState := 0, 0
	if run.Recoverable {
		recoverable = 1
	}
	if run.UnsafeState {
		unsafeState = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sysbox_runs (`+sqliteRunColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.Topology, run.Operation, run.Op, run.Status, run.Err, run.ParentID, run.Revision, run.Target, run.AgentID, recoverable, unsafeState, run.OperationKey, run.RequestFingerprint, run.Protocol, run.LeaseOwner, formatSQLiteTime(run.LeaseUntil), run.Attempt, formatSQLiteTime(run.QueuedAt), formatSQLiteTime(run.AssignedAt), formatSQLiteTime(run.StartedAt), formatSQLiteTime(run.EndedAt), formatSQLiteTime(run.DeadlineAt), run.SnapshotPath, raw)
	return err
}

func insertSQLiteAgentCommand(ctx context.Context, tx *sql.Tx, cmd controlplane.AgentCommand) error {
	return writeSQLiteCommand(ctx, tx, persistence.CommandRecord{Command: cmd}, false)
}

func (s *postgresAPIStore) CreateRunDispatch(ctx context.Context, request RunDispatchRequest) (*controlplane.Run, bool, error) {
	request = durableRunDispatch(request)
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Concurrent identical inserts can conflict on either unique index (id
	// or run_id). Resolve both through the exact request/fingerprint read below.
	tag, err := tx.Exec(ctx, `INSERT INTO sysbox_run_requests (id, fingerprint, run_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, request.Run.ID, request.Fingerprint, request.Run.ID)
	if err != nil {
		return nil, false, fmt.Errorf("postgres create run request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var storedFingerprint string
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT request.fingerprint, run.data::text FROM sysbox_run_requests request JOIN sysbox_runs run ON run.id=request.run_id WHERE request.id=$1`, request.Run.ID).Scan(&storedFingerprint, &raw)
		if err != nil {
			return nil, false, fmt.Errorf("postgres load existing run dispatch: %w", err)
		}
		if storedFingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		var run controlplane.Run
		if err := json.Unmarshal(raw, &run); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		normalizeRunProductFields(&run)
		return &run, false, nil
	}
	var existingRaw []byte
	err = tx.QueryRow(ctx, `SELECT data::text FROM sysbox_runs WHERE id=$1 FOR UPDATE`, request.Run.ID).Scan(&existingRaw)
	if err == nil {
		var existing controlplane.Run
		if err := json.Unmarshal(existingRaw, &existing); err != nil {
			return nil, false, err
		}
		if existing.RequestFingerprint != "" && existing.RequestFingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		if strings.HasPrefix(request.Fingerprint, "run-dispatch:") && request.Command.ID != "" {
			assigned, err := persistence.AssignRun(existing, request.Run, request.Fingerprint)
			if err != nil {
				return nil, false, err
			}
			assignedRaw, err := json.Marshal(assigned)
			if err != nil {
				return nil, false, err
			}
			if _, err = tx.Exec(ctx, `UPDATE sysbox_runs SET status=$2,agent_id=$3,data=$4::jsonb,updated_at=now() WHERE id=$1`, assigned.ID, assigned.Status, assigned.AgentID, string(assignedRaw)); err != nil {
				return nil, false, err
			}
			existing = assigned
			commandRaw, err := persistence.EncodeCommandRecord(persistence.CommandRecord{Command: request.Command})
			if err != nil {
				return nil, false, err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO sysbox_agent_commands (agent_id,id,status,lease_owner,lease_until,attempt,data,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,now())`, request.Command.AgentID, request.Command.ID, request.Command.Status, request.Command.LeaseOwner, nullableTime(request.Command.LeaseUntil), request.Command.Attempt, string(commandRaw)); err != nil {
				return nil, false, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		normalizeRunProductFields(&existing)
		return &existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	runRaw, err := json.Marshal(request.Run)
	if err != nil {
		return nil, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sysbox_runs (topology, id, status, agent_id, lease_owner, lease_until, attempt, data, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,now())`, request.Run.Topology, request.Run.ID, request.Run.Status, request.Run.AgentID, request.Run.LeaseOwner, nullableTime(request.Run.LeaseUntil), request.Run.Attempt, string(runRaw))
	if err != nil {
		return nil, false, fmt.Errorf("postgres create dispatched run: %w", err)
	}
	commandRaw, err := persistence.EncodeCommandRecord(persistence.CommandRecord{Command: request.Command})
	if err != nil {
		return nil, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sysbox_agent_commands (agent_id, id, status, lease_owner, lease_until, attempt, data, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,now())`, request.Command.AgentID, request.Command.ID, request.Command.Status, request.Command.LeaseOwner, nullableTime(request.Command.LeaseUntil), request.Command.Attempt, string(commandRaw))
	if err != nil {
		return nil, false, fmt.Errorf("postgres create durable run command: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	run := request.Run
	return &run, true, nil
}

func durableRunDispatch(request RunDispatchRequest) RunDispatchRequest {
	request.Run = persistence.DurableRun(request.Run)
	request.Command = persistence.DurableCommand(request.Command)
	return request
}

func (s *postgresAPIStore) GetRunDispatch(ctx context.Context, requestID, fingerprint string) (*controlplane.Run, bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Release()
	var storedFingerprint string
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT request.fingerprint, run.data::text
		FROM sysbox_run_requests request JOIN sysbox_runs run ON run.id=request.run_id
		WHERE request.id=$1`, requestID).Scan(&storedFingerprint, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if fingerprint != "" && storedFingerprint != fingerprint {
		return nil, false, errIdempotencyConflict
	}
	var run controlplane.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, false, err
	}
	normalizeRunProductFields(&run)
	return &run, true, nil
}
