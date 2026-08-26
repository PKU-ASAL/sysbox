package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"github.com/oslab/sysbox/pkg/controlplane"
)

var errIdempotencyConflict = errors.New("idempotency key was already used for a different request")

type localRunDispatch struct {
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

func (s *localAPIStore) CreateRunDispatch(_ context.Context, request RunDispatchRequest) (*controlplane.Run, bool, error) {
	dir := filepath.Join(s.runsDir, "_run_requests")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	bundle := localRunDispatch{Fingerprint: request.Fingerprint, Run: request.Run, Command: request.Command}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, false, err
	}
	tmp, err := os.CreateTemp(dir, ".dispatch-*")
	if err != nil {
		return nil, false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if err := tmp.Close(); err != nil {
		return nil, false, err
	}
	target := filepath.Join(dir, request.Run.ID+".json")
	if existing, err := s.GetRun(context.Background(), request.Run.ID); err == nil {
		if existing.RequestFingerprint != "" && existing.RequestFingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		bundle.Run = *existing
		raw, err = json.Marshal(bundle)
		if err != nil {
			return nil, false, err
		}
		if err := os.WriteFile(tmpName, raw, 0o600); err != nil {
			return nil, false, err
		}
		if err := os.Link(tmpName, target); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		return existing, false, nil
	}
	if err := os.Link(tmpName, target); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		existing, readErr := readLocalRunDispatch(target)
		if readErr != nil {
			return nil, false, readErr
		}
		if existing.Fingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		return &existing.Run, false, nil
	}
	return &bundle.Run, true, nil
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
	files, err := filepath.Glob(filepath.Join(s.runsDir, "_run_requests", "*.json"))
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
	var existingFingerprint string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint FROM sysbox_runs WHERE id=?`, request.Run.ID).Scan(&existingFingerprint)
	if err == nil {
		if existingFingerprint != "" && existingFingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		run, err := s.GetRun(ctx, request.Run.ID)
		return run, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
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

func insertSQLiteRun(ctx context.Context, tx *sql.Tx, run controlplane.Run) error {
	recoverable, unsafeState := 0, 0
	if run.Recoverable {
		recoverable = 1
	}
	if run.UnsafeState {
		unsafeState = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sysbox_runs (id, topology, operation, op, status, error, parent_id, revision, plan_id, target, agent_id, recoverable, unsafe_state, operation_key, request_fingerprint, protocol, lease_owner, lease_until, attempt, queued_at, assigned_at, started_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.Topology, run.Operation, run.Op, run.Status, run.Err, run.ParentID, run.Revision, run.PlanID, run.Target, run.AgentID, recoverable, unsafeState, run.OperationKey, run.RequestFingerprint, run.Protocol, run.LeaseOwner, formatSQLiteTime(run.LeaseUntil), run.Attempt, formatSQLiteTime(run.QueuedAt), formatSQLiteTime(run.AssignedAt), formatSQLiteTime(run.StartedAt), formatSQLiteTime(run.EndedAt))
	return err
}

func insertSQLiteAgentCommand(ctx context.Context, tx *sql.Tx, cmd controlplane.AgentCommand) error {
	runPayload, _ := json.Marshal(cmd.Run)
	sessionPayload, _ := json.Marshal(cmd.Session)
	operationPayload, _ := json.Marshal(cmd.Operation)
	executionPayload, _ := json.Marshal(struct {
		Execution *controlplane.GuestExecution       `json:"execution,omitempty"`
		Request   controlplane.GuestExecutionRequest `json:"request,omitempty"`
		FilePut   *controlplane.GuestFilePut         `json:"file_put,omitempty"`
	}{Execution: cmd.Execution, Request: cmd.ExecutionRequest, FilePut: cmd.FilePut})
	requestPayload, _ := json.Marshal(cmd.Request)
	_, err := tx.ExecContext(ctx, `INSERT INTO sysbox_agent_commands (id, agent_id, type, status, error, protocol, run_payload, session_payload, operation_payload, execution_payload, request_payload, lease_owner, lease_until, attempt, created_at, delivered, acked_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cmd.ID, cmd.AgentID, cmd.Type, cmd.Status, cmd.Err, cmd.Protocol, runPayload, sessionPayload, operationPayload, executionPayload, requestPayload, cmd.LeaseOwner, formatSQLiteTime(cmd.LeaseUntil), cmd.Attempt, formatSQLiteTime(cmd.CreatedAt), formatSQLiteTime(cmd.Delivered), formatSQLiteTime(cmd.AckedAt), formatSQLiteTime(cmd.EndedAt))
	return err
}

func (s *postgresAPIStore) CreateRunDispatch(ctx context.Context, request RunDispatchRequest) (*controlplane.Run, bool, error) {
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
	tag, err := tx.Exec(ctx, `INSERT INTO sysbox_run_requests (id, fingerprint, run_id) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`, request.Run.ID, request.Fingerprint, request.Run.ID)
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
	err = tx.QueryRow(ctx, `SELECT data::text FROM sysbox_runs WHERE id=$1`, request.Run.ID).Scan(&existingRaw)
	if err == nil {
		var existing controlplane.Run
		if err := json.Unmarshal(existingRaw, &existing); err != nil {
			return nil, false, err
		}
		if existing.RequestFingerprint != "" && existing.RequestFingerprint != request.Fingerprint {
			return nil, false, errIdempotencyConflict
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
	commandRaw, err := json.Marshal(request.Command)
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
