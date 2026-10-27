package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

type commandStorageFixture struct {
	record      *CommandRecord
	commitError error
}

func (s *commandStorageFixture) ReadCommand(context.Context, string) (*CommandRecord, error) {
	return s.record, nil
}
func (s *commandStorageFixture) ListCommands(context.Context, string) ([]CommandRecord, error) {
	if s.record == nil {
		return nil, nil
	}
	return []CommandRecord{*s.record}, nil
}
func (s *commandStorageFixture) UpdateCommand(_ context.Context, _ string, f func(*CommandRecord) (*CommandRecord, error)) (*CommandRecord, error) {
	next, err := f(s.record)
	if err != nil {
		return nil, err
	}
	if s.commitError != nil {
		return nil, s.commitError
	}
	if next != nil {
		s.record = next
	}
	return s.record, nil
}
func TestCommandRepositoryCommitFailureDoesNotGrantLease(t *testing.T) {
	failure := errors.New("commit failed")
	storage := &commandStorageFixture{record: &CommandRecord{Command: DurableCommand(controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation"})}, commitError: failure}
	repo := NewCommandRepository(storage)
	got, granted, err := repo.AcquireAgentCommandLease(context.Background(), "agent", "command", "worker", time.Minute)
	require.ErrorIs(t, err, failure)
	require.False(t, granted)
	require.Nil(t, got)
	require.Zero(t, storage.record.Command.Attempt)
}
func TestCommandRepositoryDeliveryRequiresCurrentUnexpiredGrant(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for _, mode := range []string{"expired", "wrong-owner", "old-attempt", "acked", "retired", "commit-failure"} {
		t.Run(mode, func(t *testing.T) {
			cmd := DurableCommand(controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", Status: controlplane.AgentCommandStatusLeased, LeaseOwner: "owner", LeaseUntil: now.Add(time.Minute), Attempt: 2})
			claimed := cmd
			storage := &commandStorageFixture{record: &CommandRecord{Command: cmd}}
			switch mode {
			case "expired":
				now = now.Add(2 * time.Minute)
			case "wrong-owner":
				claimed.LeaseOwner = "old"
			case "old-attempt":
				claimed.Attempt = 1
			case "acked":
				storage.record.Command.Status = controlplane.AgentCommandStatusAcknowledged
			case "retired":
				storage.record.Retired = true
			case "commit-failure":
				storage.commitError = errors.New("commit failed")
			}
			repo := NewCommandRepository(storage)
			repo.now = func() time.Time { return now }
			delivered, err := repo.PrepareAgentCommandDelivery(context.Background(), claimed)
			require.Error(t, err)
			require.Nil(t, delivered)
			require.NotEqual(t, controlplane.AgentCommandStatusDelivered, storage.record.Command.Status)
		})
	}
}
