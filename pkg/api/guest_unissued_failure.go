package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *AgentService) reconcileUnissuedGuestFailures(ctx context.Context) error {
	activities, err := s.store.ListGuestActivity(ctx, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, activity := range activities {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if activity.Kind != "guest_execution" || activity.CancelRequested || !activity.Active() {
			continue
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.reconcileUnissuedGuestFailure(call, activity.ID)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("guest delivery failure %s: %w", activity.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *AgentService) reconcileUnissuedGuestFailure(ctx context.Context, id string) error {
	x, err := s.store.GetGuestExecution(ctx, id)
	if err != nil {
		return err
	}
	if x.CancelRequested || guestExecutionTerminal(x.Status) || x.CommandID == "" {
		return nil
	}
	cmd, err := readFailureCommand(ctx, s.store, x.CommandID)
	if err != nil {
		return err
	}
	if cmd == nil || !failedDelivery(cmd.Command.Status) {
		return nil
	}
	if cmd.Command.ID != x.CommandID || cmd.Command.AgentID != x.AgentID || cmd.Command.Type != "guest_execution" {
		return persistence.ErrConflict
	}
	if !cmd.Retired {
		e := cmd.Command.Execution
		if e == nil || e.ID != x.ID || e.Topology != x.Topology || e.Node != x.Node || !e.CreatedAt.Equal(x.CreatedAt) {
			return persistence.ErrConflict
		}
		if err = s.store.DeleteAgentCommand(ctx, x.AgentID, x.CommandID); err != nil {
			return err
		}
	}
	_, err = s.store.FailUnissuedGuestExecution(ctx, id)
	return err
}
