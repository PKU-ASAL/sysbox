package controlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func RunActivityUnresolved(run controlplane.Run) bool {
	return !run.Status.IsTerminal() || run.QueuedAt.IsZero() || run.EndedAt.IsZero() || run.EndedAt.Before(run.QueuedAt)
}

func deleteActivityConflict(kind string) error {
	return fmt.Errorf("%w: unresolved %s prevents metadata deletion", persistence.ErrConflict, kind)
}

// Callers retain authority through lifecycle commit. Run and Console writers
// acquire the same exclusion before publishing new unresolved activity.
func CheckLocalDeleteActivity(ctx context.Context, store *Local, topology, uid string, except ...FenceDeleteActivityException) error {
	runs, err := store.LoadRuns(ctx)
	if err != nil {
		return err
	}
	for _, run := range LatestRunsByID(runs) {
		if run.Topology == topology && RunActivityUnresolved(run) && !skipFenceRun(run.ID, except) {
			return deleteActivityConflict("run")
		}
	}
	sessions, err := store.ListConsoleSessions(ctx, "")
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		if sess.Topology == topology && persistence.ConsoleSessionUnresolved(sess) {
			return deleteActivityConflict("console session")
		}
	}
	ops, err := persistence.NewFilesystemNodeOperationRepository(store.RunsDir).List(ctx, "")
	if err != nil {
		return err
	}
	for _, op := range ops {
		if op.Topology == topology && persistence.NodeOperationUnresolved(op) {
			return deleteActivityConflict("node operation")
		}
	}
	activities, err := persistence.NewFilesystemGuestRepository(store.RunsDir).ListGuestActivity(ctx, topology)
	if err != nil {
		return err
	}
	for _, activity := range activities {
		if activity.Unresolved() {
			return deleteActivityConflict(activity.Kind)
		}
	}
	// records() does not reacquire the record lock already held by deletion.
	commands, err := (LocalCommandStorage{Store: store}).records(ctx, "")
	if err != nil {
		return err
	}
	for _, record := range commands {
		if !record.Retired && !skipFenceCommand(record.Command.ID, except) && CommandBlocksTopology(record.Command, topology, uid) {
			return deleteActivityConflict("agent command")
		}
	}
	return nil
}

type deleteActivityRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func checkDeleteActivityRows(rows deleteActivityRows, kind, topology string, except ...FenceDeleteActivityException) error {
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if kind == "run" {
			var run *controlplane.Run
			if json.Unmarshal(raw, &run) != nil || run == nil || run.ID != id {
				return persistence.ErrCorruptRecord
			}
			if run.Topology == topology && RunActivityUnresolved(*run) && !skipFenceRun(run.ID, except) {
				return deleteActivityConflict(kind)
			}
		} else if kind == "node" {
			op, err := persistence.DecodeNodeOperation(raw)
			if err != nil {
				return err
			}
			if op.ID != id {
				return persistence.ErrCorruptRecord
			}
			if op.Topology == topology && persistence.NodeOperationUnresolved(*op) {
				return deleteActivityConflict("node operation")
			}
		} else if kind == "guest_execution" || kind == "guest_file" {
			activity, err := persistence.DecodeGuestActivity(kind, id, raw)
			if err != nil {
				return err
			}
			if activity.Topology == topology && activity.Unresolved() {
				return deleteActivityConflict(kind)
			}
		} else {
			sess, err := decodeConsoleRecord(id, raw)
			if err != nil {
				return err
			}
			if sess.Topology == topology && persistence.ConsoleSessionUnresolved(*sess) {
				return deleteActivityConflict("console session")
			}
		}
	}
	return rows.Err()
}

func CheckSQLiteDeleteActivity(ctx context.Context, conn *sql.Conn, topology, uid string, except ...FenceDeleteActivityException) error {
	for _, query := range []struct{ kind, sql string }{
		{"run", "SELECT " + SqliteRunColumns + " FROM sysbox_runs WHERE rowid IN (SELECT max(rowid) FROM sysbox_runs GROUP BY id)"},
		{"console", "SELECT id,data FROM sysbox_console_sessions"},
		{"node", "SELECT id,data FROM sysbox_node_operations"},
		{"guest_execution", "SELECT id,data FROM sysbox_guest_executions"},
		{"guest_file", "SELECT id,data FROM sysbox_guest_file_operations"},
	} {
		rows, err := conn.QueryContext(ctx, query.sql)
		if err != nil {
			return err
		}
		if query.kind == "run" {
			err = checkSQLiteDeleteRunRows(rows, topology, except...)
		} else {
			err = checkDeleteActivityRows(rows, query.kind, topology, except...)
		}
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT "+SqliteCommandColumns+" FROM sysbox_agent_commands")
	if err != nil {
		return err
	}
	err = checkDeleteCommandRows(rows, ScanSQLiteCommand, topology, uid, except...)
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func checkSQLiteDeleteRunRows(rows *sql.Rows, topology string, except ...FenceDeleteActivityException) error {
	for rows.Next() {
		run, err := ScanSQLiteRun(rows)
		if err != nil {
			return err
		}
		if run.Topology == topology && RunActivityUnresolved(*run) && !skipFenceRun(run.ID, except) {
			return deleteActivityConflict("run")
		}
	}
	return rows.Err()
}

func CheckPostgresDeleteActivity(ctx context.Context, tx pgx.Tx, topology, uid string, except ...FenceDeleteActivityException) error {
	for _, query := range []struct{ kind, sql string }{
		{"run", "SELECT id,data::text FROM sysbox_runs"},
		{"console", "SELECT id,data::text FROM sysbox_console_sessions"},
		{"node", "SELECT id,data::text FROM sysbox_node_operations"},
		{"guest_execution", "SELECT id,data::text FROM sysbox_guest_executions"},
		{"guest_file", "SELECT id,data::text FROM sysbox_guest_file_operations"},
	} {
		rows, err := tx.Query(ctx, query.sql)
		if err != nil {
			return err
		}
		err = checkDeleteActivityRows(rows, query.kind, topology, except...)
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, "SELECT "+PostgresCommandColumns+" FROM sysbox_agent_commands")
	if err != nil {
		return err
	}
	defer rows.Close()
	return checkDeleteCommandRows(rows, ScanPostgresCommand, topology, uid, except...)
}

func checkDeleteCommandRows(rows deleteActivityRows, scan func(runScanner) (*persistence.CommandRecord, error), topology, uid string, except ...FenceDeleteActivityException) error {
	for rows.Next() {
		record, err := scan(rows)
		if err != nil {
			return err
		}
		if record == nil {
			return persistence.ErrCorruptRecord
		}
		if !record.Retired && !skipFenceCommand(record.Command.ID, except) && CommandBlocksTopology(record.Command, topology, uid) {
			return deleteActivityConflict("agent command")
		}
	}
	return rows.Err()
}

// Only the reconcile-delete transaction may exclude its exact original Run and
// command; it validates and retires both under the same admission lock.
type FenceDeleteActivityException struct{ RunID, CommandID string }

func skipFenceRun(id string, except []FenceDeleteActivityException) bool {
	return len(except) == 1 && id == except[0].RunID
}

func skipFenceCommand(id string, except []FenceDeleteActivityException) bool {
	return len(except) == 1 && id == except[0].CommandID
}
