package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func enqueueGuestExecutionCancellation(ctx context.Context, store apiStore, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
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
	var storage persistence.CommandStorage
	switch s := store.(type) {
	case *localAPIStore:
		storage = localCommandStorage{store: s}
	case *sqliteAPIStore:
		storage = sqliteCommandStorage{store: s}
	case *postgresAPIStore:
		storage = postgresCommandStorage{store: s}
	default:
		return nil, persistence.ErrUnsupportedVersion
	}
	return persistence.NewCommandRepository(storage).Enqueue(ctx, cmd)
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
		if activity.Kind != "guest_execution" || !activity.CancelRequested || !activity.Active() {
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
	if !x.CancelRequested || guestExecutionTerminal(x.Status) {
		return nil
	}
	cmd, err := persistence.GuestExecutionCancellationCommand(*x)
	if err != nil {
		return err
	}
	prior, err := readFailureCommand(ctx, s.store, cmd.ID)
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return err
	}
	if prior != nil {
		if prior.Command.ID != cmd.ID || prior.Command.AgentID != cmd.AgentID || prior.Command.Type != cmd.Type {
			return persistence.ErrConflict
		}
		if prior.Retired {
			return nil
		} // reservation is not proof of remote stop
		_, err := persistence.PrepareCommandEnqueue(prior, cmd)
		return err
	}
	_, err = enqueueGuestExecutionCancellation(ctx, s.store, cmd)
	return err
}
