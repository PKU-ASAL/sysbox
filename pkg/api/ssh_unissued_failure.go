package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

func failedDelivery(status string) bool {
	return status == controlplane.AgentCommandStatusFailed || status == controlplane.AgentCommandStatusDenied || status == controlplane.AgentCommandStatusCancelled
}

// Retirement is persisted before reading authority. Grant issuance locks and
// validates the same command, so no grant can appear after this retired command
// is observed. Both current grants and stop history must be empty for this ID.
func reconcileUnissuedSSHFailure(ctx context.Context, store apiStore, command controlplane.AgentCommand) error {
	if command.Type != "node_operation" || command.ActionID == "" || !failedDelivery(command.Status) {
		return nil
	}
	record, err := readFailureCommand(ctx, store, command.ID)
	if err != nil {
		return err
	}
	if record == nil || !record.Retired || !failedDelivery(record.Command.Status) || record.Command.AgentID != command.AgentID {
		return persistence.ErrConflict
	}
	command = record.Command
	op, err := store.GetNodeOperation(ctx, command.ActionID)
	if errors.Is(err, persistence.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if op.Operation != "ssh_access_apply" && op.Operation != "ssh_access_delete" {
		return nil
	}
	history, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).HasCommandExecutionHistory(ctx, command.AgentID, command.ID)
	if err != nil || history {
		return err
	}
	transition := func(current controlplane.NodeOperation, record *persistence.CommandRecord) (controlplane.NodeOperation, error) {
		if record == nil || !record.Retired || !failedDelivery(record.Command.Status) || record.Command.ID != command.ID || record.Command.AgentID != command.AgentID || record.Command.ActionID != current.ID || current.ID != op.ID || current.CommandID != command.ID || current.AgentID != command.AgentID || current.Operation != op.Operation || current.ResourceUID != record.Command.ResourceUID || current.Generation != record.Command.Generation || record.Command.EndedAt.IsZero() {
			return current, persistence.ErrConflict
		}
		if current.Status == controlplane.NodeOperationStatusDone || current.Status == controlplane.NodeOperationStatusFailed {
			return current, nil
		}
		next := current
		next.Status = controlplane.NodeOperationStatusFailed
		next.EndedAt = record.Command.EndedAt
		next.Err = "SSH access delivery failed before execution authorization"
		if err := persistence.ValidateNodeOperationTransition(current, next); err != nil {
			return current, err
		}
		return next, nil
	}
	switch s := store.(type) {
	case *localAPIStore:
		_, err = s.completeNodeOperation(ctx, command.AgentID, op.ID, transition)
	case *sqliteAPIStore:
		_, err = s.completeNodeOperation(ctx, command.AgentID, op.ID, transition)
	case *postgresAPIStore:
		_, err = s.completeNodeOperation(ctx, command.AgentID, op.ID, transition)
	default:
		err = persistence.ErrUnsupportedVersion
	}
	return err
}

func readFailureCommand(ctx context.Context, store apiStore, id string) (*persistence.CommandRecord, error) {
	switch s := store.(type) {
	case *localAPIStore:
		return (localCommandStorage{store: s}).ReadCommand(ctx, id)
	case *sqliteAPIStore:
		return (sqliteCommandStorage{store: s}).ReadCommand(ctx, id)
	case *postgresAPIStore:
		return (postgresCommandStorage{store: s}).ReadCommand(ctx, id)
	default:
		return nil, persistence.ErrUnsupportedVersion
	}
}

// Resume the retirement-before-failure window after controller restart. Issued
// commands remain for host reconciliation; the scanner never retries effects.
func (s *AgentService) reconcileUnissuedSSHFailures(ctx context.Context) error {
	ops, err := s.store.ListNodeOperations(ctx, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, op := range ops {
		if (op.Operation != "ssh_access_apply" && op.Operation != "ssh_access_delete") || (op.Status != controlplane.NodeOperationStatusQueued && op.Status != controlplane.NodeOperationStatusRunning) || op.CommandID == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		record, err := readFailureCommand(call, s.store, op.CommandID)
		if err == nil && record != nil && record.Retired && failedDelivery(record.Command.Status) {
			err = reconcileUnissuedSSHFailure(call, s.store, record.Command)
		}
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("SSH operation %s: %w", op.ID, err))
		}
	}
	return errors.Join(failures...)
}
