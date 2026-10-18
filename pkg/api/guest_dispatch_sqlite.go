package api

import (
	"context"
	sqldriver "database/sql/driver"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

func (s *sqliteAPIStore) CreateGuestExecutionDispatch(ctx context.Context, execution controlplane.GuestExecution, command controlplane.AgentCommand) error {
	return s.createSQLiteGuestDispatch(ctx, "sysbox_guest_executions", execution.ID, func() ([]byte, error) { return persistence.EncodeGuestExecutionDispatchRecord(execution, command) }, command)
}
func (s *sqliteAPIStore) CreateGuestFileDispatch(ctx context.Context, operation controlplane.GuestFileOperation, command controlplane.AgentCommand) error {
	return s.createSQLiteGuestDispatch(ctx, "sysbox_guest_file_operations", operation.ID, func() ([]byte, error) { return persistence.EncodeGuestFileDispatchRecord(operation, command) }, command)
}
func (s *sqliteAPIStore) createSQLiteGuestDispatch(ctx context.Context, table, id string, encode func() ([]byte, error), command controlplane.AgentCommand) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
		return err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
				_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
			}
		}
	}()
	raw, err := encode()
	if err != nil {
		return err
	}
	if err = checkSQLiteMaintenanceAdmission(ctx, conn, command.TopologyUID, command.AgentID); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO "+table+"(id,data) VALUES(?,?)", id, raw); err != nil {
		return err
	}
	if err = writeSQLiteCommand(ctx, conn, persistence.CommandRecord{Command: command}, false); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
