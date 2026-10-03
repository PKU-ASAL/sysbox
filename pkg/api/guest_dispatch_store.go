package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *localAPIStore) CreateGuestExecutionDispatch(ctx context.Context, execution controlplane.GuestExecution, command controlplane.AgentCommand) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.CreateFilesystemGuestDispatch(ctx, &execution, nil, command)
}
