package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleHostFenceRelease(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	var body struct {
		ObservationCommandID string `json:"observation_command_id"`
	}
	if key == "" || len(key) > 128 {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
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
	result, err := s.releaseHostFence(r.Context(), fence, persistence.HostFenceReleaseRequest{FenceID: fence.Request.ID, Key: key, ObservationCommandID: body.ObservationCommandID})
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) releaseHostFence(ctx context.Context, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.HostFenceRelease, error) {
	var result persistence.HostFenceRelease
	topology, uid := fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID
	switch store := s.apiStore.(type) {
	case *localAPIStore:
		unlock, err := store.lockRunRecords(ctx)
		if err != nil {
			return result, err
		}
		defer unlock()
		repo, err := s.actionRepository(topology)
		if err != nil {
			return result, err
		}
		return repo.CommitFilesystemHostFenceRelease(ctx, store.runsDir, topology, request, s.grantHostKeys[fence.Request.Grant.AgentID], func() (controlplane.Run, persistence.CommandRecord, error) {
			if err := checkLocalDeleteActivity(ctx, store, topology, uid); err != nil {
				return controlplane.Run{}, persistence.CommandRecord{}, err
			}
			run, err := store.GetRun(ctx, fence.Request.RunID)
			if err != nil {
				return controlplane.Run{}, persistence.CommandRecord{}, err
			}
			command, err := (localCommandStorage{store: store}).read(ctx, request.ObservationCommandID)
			if err != nil {
				return controlplane.Run{}, persistence.CommandRecord{}, err
			}
			if command == nil {
				return controlplane.Run{}, persistence.CommandRecord{}, persistence.ErrNotFound
			}
			return *run, *command, nil
		})
	case *sqliteAPIStore:
		return s.releaseSQLiteHostFence(ctx, store, fence, request)
	case *postgresAPIStore:
		return s.releasePostgresHostFence(ctx, store, fence, request)
	default:
		return result, persistence.ErrUnsupportedVersion
	}
}

func (s *Server) releaseSQLiteHostFence(ctx context.Context, store *sqliteAPIStore, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.HostFenceRelease, error) {
	var zero persistence.HostFenceRelease
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
	prior, err := persistence.ReplayHostFenceRelease(raw, request)
	if err != nil {
		return zero, err
	}
	if prior != nil {
		return *prior, nil
	}
	if err = checkSQLiteDeleteActivity(ctx, conn, fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID); err != nil {
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
	next, result, err := persistence.ApplyHostFenceRelease(raw, lifecycle, *run, *command, s.grantHostKeys[fence.Request.Grant.AgentID], request, time.Now().UTC())
	if err != nil {
		return zero, err
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
func (s *Server) releasePostgresHostFence(ctx context.Context, store *postgresAPIStore, fence persistence.HostFenceRecord, request persistence.HostFenceReleaseRequest) (persistence.HostFenceRelease, error) {
	var zero persistence.HostFenceRelease
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
	prior, err := persistence.ReplayHostFenceRelease(raw, request)
	if err != nil {
		return zero, err
	}
	if prior != nil {
		return *prior, nil
	}
	if err = checkPostgresDeleteActivity(ctx, tx, fence.Request.Topology, fence.Request.Grant.Scope.TopologyUID); err != nil {
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
	next, result, err := persistence.ApplyHostFenceRelease(raw, lifecycle, run, *command, s.grantHostKeys[fence.Request.Grant.AgentID], request, time.Now().UTC())
	if err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, "UPDATE sysbox_execution_authority SET data=$1::jsonb WHERE id='authority'", string(next)); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return result, nil
}
