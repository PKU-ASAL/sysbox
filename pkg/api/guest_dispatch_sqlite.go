package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *sqliteAPIStore) CreateGuestExecutionDispatch(ctx context.Context, execution controlplane.GuestExecution, command controlplane.AgentCommand) error {
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
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	raw, err := persistence.EncodeGuestExecutionDispatchRecord(execution, command)
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO sysbox_guest_executions(id,data) VALUES(?,?)", execution.ID, raw); err != nil {
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

func (s *postgresAPIStore) CreateGuestExecutionDispatch(context.Context, controlplane.GuestExecution, controlplane.AgentCommand) error {
	return persistence.ErrInvalidRecord
}
