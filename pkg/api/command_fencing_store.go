package api

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func validCommandTransition(expected, next controlplane.AgentCommand) bool {
	return !expected.IsTerminal() && expected.ID == next.ID && expected.AgentID == next.AgentID && expected.LeaseOwner == next.LeaseOwner && expected.Attempt == next.Attempt && expected.LeaseUntil.Equal(next.LeaseUntil) && controlplane.CanAdvanceAgentCommandStatus(expected.Status, next.Status)
}
func (s *sqliteAPIStore) CompareAndSwapAgentCommand(ctx context.Context, expected, next controlplane.AgentCommand) (bool, error) {
	if !validCommandTransition(expected, next) {
		return false, nil
	}
	db, err := s.open()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `UPDATE sysbox_agent_commands SET status=?,error=?,acked_at=?,ended_at=?,delivered=? WHERE id=? AND agent_id=? AND status=? AND lease_owner=? AND attempt=? AND lease_until=?`, next.Status, next.Err, formatSQLiteTime(next.AckedAt), formatSQLiteTime(next.EndedAt), formatSQLiteTime(next.Delivered), expected.ID, expected.AgentID, expected.Status, expected.LeaseOwner, expected.Attempt, formatSQLiteTime(expected.LeaseUntil))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (s *postgresAPIStore) CompareAndSwapAgentCommand(ctx context.Context, expected, next controlplane.AgentCommand) (bool, error) {
	if !validCommandTransition(expected, next) {
		return false, nil
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	raw, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	result, err := conn.Exec(ctx, `UPDATE sysbox_agent_commands SET status=$7,data=$8::jsonb,updated_at=now() WHERE id=$1 AND agent_id=$2 AND status=$3 AND lease_owner=$4 AND attempt=$5 AND lease_until IS NOT DISTINCT FROM $6`, expected.ID, expected.AgentID, expected.Status, expected.LeaseOwner, expected.Attempt, nullableTime(expected.LeaseUntil), next.Status, string(raw))
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}
func (s *localAPIStore) CompareAndSwapAgentCommand(ctx context.Context, expected, next controlplane.AgentCommand) (bool, error) {
	if !validCommandTransition(expected, next) {
		return false, nil
	}
	localRunTransitionMu.Lock()
	defer localRunTransitionMu.Unlock()
	commands, err := s.ListAgentCommands(ctx, expected.AgentID)
	if err != nil {
		return false, err
	}
	for _, current := range commands {
		if current.ID == expected.ID {
			if current.Status != expected.Status || current.LeaseOwner != expected.LeaseOwner || current.Attempt != expected.Attempt || !current.LeaseUntil.Equal(expected.LeaseUntil) {
				return false, nil
			}
			return true, s.SaveAgentCommand(ctx, next)
		}
	}
	return false, nil
}
