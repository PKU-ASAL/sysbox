package controlstore

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

type SqliteCommandStorage struct{ Store *SQLite }

const SqliteCommandColumns = "id, agent_id, type, status, error, protocol, run_payload, session_payload, operation_payload, execution_payload, request_payload, lease_owner, lease_until, attempt, created_at, delivered, acked_at, ended_at, record_json"

type commandExecutionPayload struct {
	Execution *controlplane.GuestExecution       `json:"execution,omitempty"`
	Request   controlplane.GuestExecutionRequest `json:"request,omitempty"`
	FilePut   *controlplane.GuestFilePut         `json:"file_put,omitempty"`
}

func ScanSQLiteCommand(row runScanner) (*persistence.CommandRecord, error) {
	var cmd controlplane.AgentCommand
	var run, session, operation, execution, request, full []byte
	var lease, created, delivered, acked, ended string
	err := row.Scan(&cmd.ID, &cmd.AgentID, &cmd.Type, &cmd.Status, &cmd.Err, &cmd.Protocol, &run, &session, &operation, &execution, &request, &cmd.LeaseOwner, &lease, &cmd.Attempt, &created, &delivered, &acked, &ended, &full)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var payload commandExecutionPayload
	for _, field := range []struct {
		raw    []byte
		target any
	}{{run, &cmd.Run}, {session, &cmd.Session}, {operation, &cmd.Operation}, {execution, &payload}, {request, &cmd.Request}} {
		if len(field.raw) > 0 && json.Unmarshal(field.raw, field.target) != nil {
			return nil, persistence.ErrCorruptRecord
		}
	}
	cmd.Execution = payload.Execution
	cmd.ExecutionRequest = payload.Request
	cmd.FilePut = payload.FilePut
	for _, field := range []struct {
		raw    string
		target *time.Time
	}{{lease, &cmd.LeaseUntil}, {created, &cmd.CreatedAt}, {delivered, &cmd.Delivered}, {acked, &cmd.AckedAt}, {ended, &cmd.EndedAt}} {
		if field.raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, field.raw)
		if err != nil {
			return nil, persistence.ErrCorruptRecord
		}
		*field.target = parsed
	}
	indexed, err := persistence.EncodeCommandRecord(persistence.CommandRecord{Command: cmd})
	if err != nil {
		return nil, err
	}
	if len(full) == 0 {
		return nil, fmt.Errorf("%w: command %q requires explicit rebuild", persistence.ErrUnsupportedVersion, cmd.ID)
	}
	record, err := persistence.DecodeCommandRecord(full)
	if err != nil {
		return nil, err
	}
	// Fencing fields live only in the full snapshot, not legacy index columns.
	indexedCommand := record.Command
	indexedCommand.ActionID, indexedCommand.ResourceUID = "", ""
	indexedCommand.RecoveryMode = ""
	indexedCommand.TopologyUID = ""
	indexedCommand.ExpectedSerial = 0
	indexedCommand.Generation, indexedCommand.LeaseEpoch = 0, 0
	// GC candidate identity is persisted in the full snapshot; legacy index
	// columns do not carry it, so exclude it from the index consistency check.
	indexedCommand.GCKind, indexedCommand.GCExternalID, indexedCommand.GCInventoryDigest = "", "", ""
	comparable, err := persistence.EncodeCommandRecord(persistence.CommandRecord{Command: indexedCommand})
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(indexed, comparable) {
		return nil, persistence.ErrCorruptRecord
	}
	return record, nil
}

// The domain callback owns transition and lease policy; SQL only serializes
// access, checks the complete persisted snapshot, and commits the replacement.
func (b SqliteCommandStorage) UpdateCommand(ctx context.Context, id string, transform func(*persistence.CommandRecord) (*persistence.CommandRecord, error)) (*persistence.CommandRecord, error) {
	db, err := b.Store.OpenDB()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return nil, err
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
	current, err := ScanSQLiteCommand(conn.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", id))
	if err != nil {
		return nil, err
	}
	next, err := transform(current)
	if err != nil {
		return nil, err
	}
	if next != nil {
		authority, err := readSQLiteAuthority(ctx, conn)
		if err != nil {
			return nil, err
		}
		if err := persistence.ValidateCommandMaintenance(authority, current, *next, func(topology string) error {
			return checkSQLiteTopologyMaintenance(ctx, conn, topology)
		}); err != nil {
			return nil, err
		}
		if err := WriteSQLiteCommand(ctx, conn, *next, true); err != nil {
			return nil, err
		}
		current = next
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true
	return current, nil
}

func (b SqliteCommandStorage) ReadCommand(ctx context.Context, id string) (*persistence.CommandRecord, error) {
	db, err := b.Store.OpenDB()
	if err != nil {
		return nil, err
	}
	return ScanSQLiteCommand(db.QueryRowContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands WHERE id=?", id))
}

func (b SqliteCommandStorage) ListCommands(ctx context.Context, agent string) ([]persistence.CommandRecord, error) {
	db, err := b.Store.OpenDB()
	if err != nil {
		return nil, err
	}
	query := "SELECT " + SqliteCommandColumns + " FROM sysbox_agent_commands"
	args := []any{}
	if agent != "" {
		query += " WHERE agent_id=?"
		args = append(args, agent)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []persistence.CommandRecord
	for rows.Next() {
		record, err := ScanSQLiteCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, rows.Err()
}

func WriteSQLiteCommand(ctx context.Context, writer sqliteRunWriter, record persistence.CommandRecord, replace bool) error {
	cmd := persistence.DurableCommand(record.Command)
	record.Command = cmd
	full, err := persistence.EncodeCommandRecord(record)
	if err != nil {
		return err
	}
	payloads := make([][]byte, 5)
	for i, value := range []any{cmd.Run, cmd.Session, cmd.Operation, commandExecutionPayload{Execution: cmd.Execution, Request: cmd.ExecutionRequest, FilePut: cmd.FilePut}, cmd.Request} {
		payloads[i], err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	query := "INSERT INTO sysbox_agent_commands (" + SqliteCommandColumns + ") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
	if replace {
		query += " ON CONFLICT(id) DO UPDATE SET agent_id=excluded.agent_id,type=excluded.type,status=excluded.status,error=excluded.error,protocol=excluded.protocol,run_payload=excluded.run_payload,session_payload=excluded.session_payload,operation_payload=excluded.operation_payload,execution_payload=excluded.execution_payload,request_payload=excluded.request_payload,lease_owner=excluded.lease_owner,lease_until=excluded.lease_until,attempt=excluded.attempt,created_at=excluded.created_at,delivered=excluded.delivered,acked_at=excluded.acked_at,ended_at=excluded.ended_at,record_json=excluded.record_json"
	}
	_, err = writer.ExecContext(ctx, query, cmd.ID, cmd.AgentID, cmd.Type, cmd.Status, cmd.Err, cmd.Protocol, payloads[0], payloads[1], payloads[2], payloads[3], payloads[4], cmd.LeaseOwner, FormatSQLiteTime(cmd.LeaseUntil), cmd.Attempt, FormatSQLiteTime(cmd.CreatedAt), FormatSQLiteTime(cmd.Delivered), FormatSQLiteTime(cmd.AckedAt), FormatSQLiteTime(cmd.EndedAt), full)
	return err
}
