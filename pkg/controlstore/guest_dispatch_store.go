package controlstore

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) CreateGuestExecutionDispatch(ctx context.Context, execution controlplane.GuestExecution, command controlplane.AgentCommand) error {
	if _, err := persistence.EncodeGuestExecutionDispatch(execution, command); err != nil {
		return err
	}
	unlock, err := s.LockRunRecords(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := (LocalCommandStorage{Store: s}).Read(ctx, command.ID)
	if err != nil {
		return err
	}
	if current != nil {
		return persistence.ErrConflict
	}
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return persistence.FilesystemExecutionGrantStorage(s.RunsDir).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		if err := persistence.ValidateMaintenanceWrite(raw, command.TopologyUID, command.AgentID); err != nil {
			return nil, err
		}
		return nil, repo.CreateFilesystemGuestDispatchUnderAuthority(ctx, &execution, nil, command)
	})
}

func (s *Local) CreateGuestFileDispatch(ctx context.Context, operation controlplane.GuestFileOperation, command controlplane.AgentCommand) error {
	if _, err := persistence.EncodeGuestFileDispatch(operation, command); err != nil {
		return err
	}
	unlock, err := s.LockRunRecords(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := (LocalCommandStorage{Store: s}).Read(ctx, command.ID)
	if err != nil {
		return err
	}
	if current != nil {
		return persistence.ErrConflict
	}
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return persistence.FilesystemExecutionGrantStorage(s.RunsDir).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		if err := persistence.ValidateMaintenanceWrite(raw, command.TopologyUID, command.AgentID); err != nil {
			return nil, err
		}
		return nil, repo.CreateFilesystemGuestDispatchUnderAuthority(ctx, nil, &operation, command)
	})
}
