package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *localAPIStore) CreateGuestExecutionDispatch(ctx context.Context, execution controlplane.GuestExecution, command controlplane.AgentCommand) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.CreateFilesystemGuestDispatch(ctx, &execution, nil, command)
}
func (s *sqliteAPIStore) CreateGuestExecutionDispatch(context.Context, controlplane.GuestExecution, controlplane.AgentCommand) error {
	return persistence.ErrInvalidRecord
}
func (s *postgresAPIStore) CreateGuestExecutionDispatch(context.Context, controlplane.GuestExecution, controlplane.AgentCommand) error {
	return persistence.ErrInvalidRecord
}
