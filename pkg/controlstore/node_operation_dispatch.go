package controlstore

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) CreateNodeOperationDispatch(ctx context.Context, op controlplane.NodeOperation, cmd controlplane.AgentCommand) error {
	if _, err := persistence.EncodeNodeOperationDispatch(op, cmd); err != nil {
		return err
	}
	unlock, err := s.LockRunRecords(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := (LocalCommandStorage{Store: s}).Read(ctx, cmd.ID)
	if err != nil {
		return err
	}
	if current != nil {
		return persistence.ErrConflict
	}
	repo, err := s.nodeOperationRepository(ctx)
	if err != nil {
		return err
	}
	return persistence.FilesystemExecutionGrantStorage(s.RunsDir).UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		if err := persistence.ValidateMaintenanceWrite(raw, cmd.TopologyUID, cmd.AgentID); err != nil {
			return nil, err
		}
		return nil, repo.CreateFilesystemDispatchUnderAuthority(ctx, op, cmd)
	})
}

func (s *SQLite) CreateNodeOperationDispatch(ctx context.Context, op controlplane.NodeOperation, cmd controlplane.AgentCommand) error {
	return s.createSQLiteGuestDispatch(ctx, "sysbox_node_operations", op.ID, func() ([]byte, error) { return persistence.EncodeNodeOperationDispatch(op, cmd) }, cmd)
}

func (s *Postgres) CreateNodeOperationDispatch(ctx context.Context, op controlplane.NodeOperation, cmd controlplane.AgentCommand) error {
	return s.createPostgresGuestDispatch(ctx, "sysbox_node_operations", op.ID, func() ([]byte, error) { return persistence.EncodeNodeOperationDispatch(op, cmd) }, cmd)
}
