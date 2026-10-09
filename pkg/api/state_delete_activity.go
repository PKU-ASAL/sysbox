package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func runActivityUnresolved(run controlplane.Run) bool {
	return !run.Status.IsTerminal() || run.QueuedAt.IsZero() || run.EndedAt.IsZero() || run.EndedAt.Before(run.QueuedAt)
}

func deleteActivityConflict(kind string) error {
	return fmt.Errorf("%w: unresolved %s prevents metadata deletion", persistence.ErrConflict, kind)
}

// Callers retain authority through lifecycle commit. Run and Console writers
// acquire the same exclusion before publishing new unresolved activity.
func checkLocalDeleteActivity(ctx context.Context, store *localAPIStore, topology, uid string) error {
	runs, err := store.LoadRuns(ctx)
	if err != nil {
		return err
	}
	for _, run := range latestRunsByID(runs) {
		if run.Topology == topology && runActivityUnresolved(run) {
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
	ops, err := persistence.NewFilesystemNodeOperationRepository(store.runsDir).List(ctx, "")
	if err != nil {
		return err
	}
	for _, op := range ops {
		if op.Topology == topology && persistence.NodeOperationUnresolved(op) {
			return deleteActivityConflict("node operation")
		}
	}
	// records() does not reacquire the record lock already held by deletion.
	commands, err := (localCommandStorage{store: store}).records(ctx, "")
	if err != nil {
		return err
	}
	for _, record := range commands {
		if !record.Retired && commandBlocksTopology(record.Command, topology, uid) {
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

func checkDeleteActivityRows(rows deleteActivityRows, kind, topology string) error {
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
			if run.Topology == topology && runActivityUnresolved(*run) {
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

func checkSQLiteDeleteActivity(ctx context.Context, conn *sql.Conn, topology, uid string) error {
	for _, query := range []struct{ kind, sql string }{
		{"run", "SELECT " + sqliteRunColumns + " FROM sysbox_runs WHERE rowid IN (SELECT max(rowid) FROM sysbox_runs GROUP BY id)"},
		{"console", "SELECT id,data FROM sysbox_console_sessions"},
		{"node", "SELECT id,data FROM sysbox_node_operations"},
	} {
		rows, err := conn.QueryContext(ctx, query.sql)
		if err != nil {
			return err
		}
		if query.kind == "run" {
			err = checkSQLiteDeleteRunRows(rows, topology)
		} else {
			err = checkDeleteActivityRows(rows, query.kind, topology)
		}
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT "+sqliteCommandColumns+" FROM sysbox_agent_commands")
	if err != nil {
		return err
	}
	err = checkDeleteCommandRows(rows, scanSQLiteCommand, topology, uid)
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func checkSQLiteDeleteRunRows(rows *sql.Rows, topology string) error {
	for rows.Next() {
		run, err := scanSQLiteRun(rows)
		if err != nil {
			return err
		}
		if run.Topology == topology && runActivityUnresolved(*run) {
			return deleteActivityConflict("run")
		}
	}
	return rows.Err()
}

func checkPostgresDeleteActivity(ctx context.Context, tx pgx.Tx, topology, uid string) error {
	for _, query := range []struct{ kind, sql string }{
		{"run", "SELECT id,data::text FROM sysbox_runs"},
		{"console", "SELECT id,data::text FROM sysbox_console_sessions"},
		{"node", "SELECT id,data::text FROM sysbox_node_operations"},
	} {
		rows, err := tx.Query(ctx, query.sql)
		if err != nil {
			return err
		}
		err = checkDeleteActivityRows(rows, query.kind, topology)
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands")
	if err != nil {
		return err
	}
	defer rows.Close()
	return checkDeleteCommandRows(rows, scanPostgresCommand, topology, uid)
}

func checkDeleteCommandRows(rows deleteActivityRows, scan func(runScanner) (*persistence.CommandRecord, error), topology, uid string) error {
	for rows.Next() {
		record, err := scan(rows)
		if err != nil {
			return err
		}
		if record == nil {
			return persistence.ErrCorruptRecord
		}
		if !record.Retired && commandBlocksTopology(record.Command, topology, uid) {
			return deleteActivityConflict("agent command")
		}
	}
	return rows.Err()
}
