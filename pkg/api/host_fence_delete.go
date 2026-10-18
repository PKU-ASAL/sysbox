package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleFenceDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	var body struct {
		ObservationCommandID string `json:"observation_command_id"`
	}
	if err := decodeFenceBody(w, r, &body); err != nil {
		writeError(w, 400, err)
		return
	}
	if validatePathSegment(body.ObservationCommandID, "observation") != nil {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	fence, err := s.readHostFence(r)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	request := persistence.HostFenceReleaseRequest{FenceID: fence.Request.ID, Key: key, ObservationCommandID: body.ObservationCommandID}
	if rec := fence.Reconciliation; rec != nil && rec.Completed {
		if rec.Key != key || rec.ObservationCommandID != body.ObservationCommandID {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		writeJSON(w, 200, rec)
		return
	}
	result, err := s.reconcileFenceDelete(r.Context(), fence, request)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) reconcileFenceDelete(ctx context.Context, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.FenceDeleteReconciliation, error) {
	var zero persistence.FenceDeleteReconciliation
	switch store := s.apiStore.(type) {
	case *localAPIStore:
		unlock, err := store.lockRunRecords(ctx)
		if err != nil {
			return zero, err
		}
		defer unlock()
		repo, err := s.actionRepository(fence.Request.Topology)
		if err != nil {
			return zero, err
		}
		return repo.CommitFilesystemFenceDelete(ctx, store.runsDir, fence.Request.Topology, request, s.grantHostKeys[fence.Request.Grant.AgentID], func() (controlplane.Run, persistence.CommandRecord, persistence.CommandRecord, error) {
			var run controlplane.Run
			var original, obs persistence.CommandRecord
			if err := checkLocalDeleteActivity(ctx, store, fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID, fenceDeleteActivityException{RunID: fence.Request.RunID, CommandID: fence.Request.Grant.CommandID}); err != nil {
				return run, original, obs, err
			}
			r, err := store.GetRun(ctx, fence.Request.RunID)
			if err != nil {
				return run, original, obs, err
			}
			o, err := (localCommandStorage{store: store}).read(ctx, fence.Request.Grant.CommandID)
			if err != nil {
				return run, original, obs, err
			}
			c, err := (localCommandStorage{store: store}).read(ctx, request.ObservationCommandID)
			if err != nil {
				return run, original, obs, err
			}
			if o == nil || c == nil {
				return run, original, obs, persistence.ErrNotFound
			}
			return *r, *o, *c, nil
		}, func(run controlplane.Run, cmd persistence.CommandRecord) error {
			if err := store.saveFenceDeleteRun(ctx, run); err != nil {
				return err
			}
			raw, err := persistence.EncodeCommandRecord(cmd)
			if err != nil {
				return err
			}
			return writeLocalObject(filepath.Join(store.runsDir, "_agents", cmd.Command.AgentID, "commands", cmd.Command.ID+".json"), json.RawMessage(raw))
		})
	case *sqliteAPIStore:
		return s.reconcileSQLiteFenceDelete(ctx, store, fence, request)
	case *postgresAPIStore:
		return s.reconcilePostgresFenceDelete(ctx, store, fence, request)
	default:
		return zero, persistence.ErrUnsupportedVersion
	}
}

func (s *Server) reconcileSQLiteFenceDelete(ctx context.Context, store *sqliteAPIStore, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.FenceDeleteReconciliation, error) {
	var zero persistence.FenceDeleteReconciliation
	db, err := store.open()
	if err != nil {
		return zero, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return zero, err
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
	var raw, lifecycle []byte
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw); err != nil {
		return zero, err
	}
	if err = checkSQLiteDeleteActivity(ctx, conn, fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID, fenceDeleteActivityException{RunID: fence.Request.RunID, CommandID: fence.Request.Grant.CommandID}); err != nil {
		return zero, err
	}
	if err = conn.QueryRowContext(ctx, "SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?", fence.Request.Topology).Scan(&lifecycle); err != nil {
		return zero, err
	}
	run, err := scanSQLiteRun(conn.QueryRowContext(ctx, "SELECT "+sqliteRunColumns+" FROM sysbox_runs WHERE id=? ORDER BY rowid DESC LIMIT 1", fence.Request.RunID))
	if err != nil {
		return zero, err
	}
	command, err := scanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+sqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", request.ObservationCommandID))
	if err != nil {
		return zero, err
	}
	if run == nil || command == nil {
		return zero, persistence.ErrNotFound
	}
	original, err := scanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+sqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", fence.Request.Grant.CommandID))
	if err != nil {
		return zero, err
	}
	if original == nil {
		return zero, persistence.ErrNotFound
	}
	transition, err := persistence.PrepareFenceDelete(raw, lifecycle, *run, *original, *command, s.grantHostKeys[fence.Request.Grant.AgentID], request, time.Now().UTC())
	if err != nil {
		return zero, err
	}
	if transition.Completed {
		record, err := readFenceReconciliation(raw, request.FenceID)
		return record, err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology=?", transition.Lifecycle, fence.Request.Topology); err != nil {
		return zero, err
	}
	if err = insertSQLiteRun(ctx, conn, transition.Run); err != nil {
		return zero, err
	}
	if err = writeSQLiteCommand(ctx, conn, transition.Command, true); err != nil {
		return zero, err
	}
	next, err := persistence.FinishFenceDelete(transition.PreparedAuthority, transition.Lifecycle, transition.Run, transition.Command, request)
	if err != nil {
		return zero, err
	}
	result, resultErr := readFenceReconciliation(next, request.FenceID)
	if resultErr != nil {
		return zero, resultErr
	}
	if _, err = conn.ExecContext(ctx, "UPDATE sysbox_execution_authority SET data=? WHERE id='authority'", next); err != nil {
		return zero, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return zero, err
	}
	committed = true
	return result, nil
}
func (s *Server) reconcilePostgresFenceDelete(ctx context.Context, store *postgresAPIStore, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.FenceDeleteReconciliation, error) {
	var zero persistence.FenceDeleteReconciliation
	pool, err := store.poolFor(ctx)
	if err != nil {
		return zero, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('sysbox-run-admission'))"); err != nil {
		return zero, err
	}
	var raw, lifecycle, runRaw []byte
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_execution_authority WHERE id='authority' FOR UPDATE").Scan(&raw); err != nil {
		return zero, err
	}
	if err = checkPostgresDeleteActivity(ctx, tx, fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID, fenceDeleteActivityException{RunID: fence.Request.RunID, CommandID: fence.Request.Grant.CommandID}); err != nil {
		return zero, err
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_lifecycle_aggregates WHERE topology=$1 FOR UPDATE", fence.Request.Topology).Scan(&lifecycle); err != nil {
		return zero, err
	}
	if err = tx.QueryRow(ctx, "SELECT data::text FROM sysbox_runs WHERE id=$1 FOR UPDATE", fence.Request.RunID).Scan(&runRaw); err != nil {
		return zero, err
	}
	var run controlplane.Run
	if json.Unmarshal(runRaw, &run) != nil {
		return zero, persistence.ErrCorruptRecord
	}
	command, err := scanPostgresCommand(tx.QueryRow(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", request.ObservationCommandID))
	if err != nil {
		return zero, err
	}
	if command == nil {
		return zero, persistence.ErrNotFound
	}
	original, err := scanPostgresCommand(tx.QueryRow(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", fence.Request.Grant.CommandID))
	if err != nil {
		return zero, err
	}
	if original == nil {
		return zero, persistence.ErrNotFound
	}
	transition, err := persistence.PrepareFenceDelete(raw, lifecycle, run, *original, *command, s.grantHostKeys[fence.Request.Grant.AgentID], request, time.Now().UTC())
	if err != nil {
		return zero, err
	}
	if transition.Completed {
		record, err := readFenceReconciliation(raw, request.FenceID)
		return record, err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology=$2", string(transition.Lifecycle), fence.Request.Topology); err != nil {
		return zero, err
	}
	updated := transition.Run
	encoded, err := json.Marshal(updated)
	if err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sysbox_runs SET status=$2,lease_until=$3,data=$4::jsonb,updated_at=now() WHERE id=$1`, updated.ID, updated.Status, nullableTime(updated.LeaseUntil), string(encoded)); err != nil {
		return zero, err
	}
	encoded, err = persistence.EncodeCommandRecord(transition.Command)
	if err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sysbox_agent_commands SET status=$2,data=$3::jsonb WHERE id=$1`, transition.Command.Command.ID, transition.Command.Command.Status, string(encoded)); err != nil {
		return zero, err
	}
	next, err := persistence.FinishFenceDelete(transition.PreparedAuthority, transition.Lifecycle, transition.Run, transition.Command, request)
	if err != nil {
		return zero, err
	}
	result, resultErr := readFenceReconciliation(next, request.FenceID)
	if resultErr != nil {
		return zero, resultErr
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb WHERE id='authority'", string(next)); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return result, nil
}

func readFenceReconciliation(raw []byte, id string) (persistence.FenceDeleteReconciliation, error) {
	memory := &grantTransaction{raw: raw}
	record, err := persistence.NewExecutionGrantRepository(memory).ReadHostFence(context.Background(), id)
	if err != nil {
		return persistence.FenceDeleteReconciliation{}, err
	}
	if record.Reconciliation == nil {
		return persistence.FenceDeleteReconciliation{}, persistence.ErrConflict
	}
	return *record.Reconciliation, nil
}
