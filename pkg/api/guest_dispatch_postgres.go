package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *postgresAPIStore) CreateGuestExecutionDispatch(ctx context.Context, x controlplane.GuestExecution, c controlplane.AgentCommand) error {
	return s.createPostgresGuestDispatch(ctx, "sysbox_guest_executions", x.ID, func() ([]byte, error) { return persistence.EncodeGuestExecutionDispatchRecord(x, c) }, c)
}
func (s *postgresAPIStore) CreateGuestFileDispatch(ctx context.Context, x controlplane.GuestFileOperation, c controlplane.AgentCommand) error {
	return s.createPostgresGuestDispatch(ctx, "sysbox_guest_file_operations", x.ID, func() ([]byte, error) { return persistence.EncodeGuestFileDispatchRecord(x, c) }, c)
}
func (s *postgresAPIStore) createPostgresGuestDispatch(ctx context.Context, table, id string, encode func() ([]byte, error), cmd controlplane.AgentCommand) error {
	cmd = persistence.DurableCommand(cmd)
	pool, err := s.poolFor(ctx)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	raw, err := encode()
	if err != nil {
		return err
	}
	command, err := persistence.EncodeCommandRecord(persistence.CommandRecord{Command: cmd})
	if err != nil {
		return err
	}
	if err = checkPostgresMaintenanceAdmission(ctx, tx, cmd.TopologyUID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO "+table+"(id,data) VALUES($1,$2::jsonb)", id, string(raw)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO sysbox_agent_commands(agent_id,id,status,lease_owner,lease_until,attempt,data,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,now())", cmd.AgentID, cmd.ID, cmd.Status, cmd.LeaseOwner, nullableTime(cmd.LeaseUntil), cmd.Attempt, string(command)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
