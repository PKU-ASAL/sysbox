package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Notification may reference an already committed cancellation, never create
// one outside the operation/outbox transaction.
func committedGuestExecutionCancellation(ctx context.Context, store controlstore.Store, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	cmd = persistence.DurableCommand(cmd)
	if cmd.CreatedAt.IsZero() {
		cmd.CreatedAt = time.Now().UTC()
	}
	x, err := store.GetGuestExecution(ctx, cmd.Operation.ExternalID)
	if err != nil {
		return nil, err
	}
	if !x.CancelRequested || guestExecutionTerminal(x.Status) {
		return nil, persistence.ErrConflict
	}
	expected, err := persistence.GuestExecutionCancellationCommand(*x)
	if err != nil {
		return nil, err
	}
	if _, err := persistence.PrepareCommandEnqueue(&persistence.CommandRecord{Command: cmd}, expected); err != nil {
		return nil, err
	}
	prior, err := readFailureCommand(ctx, store, cmd.ID)
	if err != nil {
		return nil, err
	}
	if prior == nil || prior.Retired {
		return nil, persistence.ErrConflict
	}
	if _, err := persistence.PrepareCommandEnqueue(prior, expected); err != nil {
		return nil, err
	}
	return &prior.Command, nil
}

func (s *AgentService) reconcileGuestExecutionCancellations(ctx context.Context) error {
	activities, err := s.store.ListGuestActivity(ctx, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, activity := range activities {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if activity.Kind != "guest_execution" || !activity.CancelRequested {
			continue
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.reconcileGuestExecutionCancellation(call, activity.ID)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("guest cancellation %s: %w", activity.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *AgentService) reconcileGuestExecutionCancellation(ctx context.Context, id string) error {
	x, err := s.store.GetGuestExecution(ctx, id)
	if err != nil {
		return err
	}
	if !x.CancelRequested {
		return nil
	}
	// Replay also repairs file authority revocation after an interrupted
	// record/authority publication, including a retired cancellation delivery.
	x, err = s.store.CancelGuestExecution(ctx, x.ID)
	if err != nil {
		return err
	}
	if x.CommandID == "" {
		return nil
	}
	// Retirement and cancellation intent are durable before asking the shared
	// authority transaction to certify that execution was never authorized.
	if err = s.store.DeleteAgentCommand(ctx, x.AgentID, x.CommandID); err != nil {
		return err
	}
	x, err = s.store.CancelGuestExecution(ctx, x.ID)
	if err != nil {
		return err
	}
	if x.Status == controlplane.GuestExecutionCancelled && x.ResultClass == controlplane.GuestExecutionResultClassNotStarted {
		cmd, err := persistence.GuestExecutionCancellationCommand(*x)
		if err != nil {
			return err
		}
		return s.store.DeleteAgentCommand(ctx, x.AgentID, cmd.ID)
	}
	return nil
}
