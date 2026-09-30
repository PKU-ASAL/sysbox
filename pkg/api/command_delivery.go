package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *localAPIStore) PrepareAgentCommandDelivery(ctx context.Context, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(localCommandStorage{store: s}).PrepareAgentCommandDelivery(ctx, cmd)
}
func (s *sqliteAPIStore) PrepareAgentCommandDelivery(ctx context.Context, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(sqliteCommandStorage{store: s}).PrepareAgentCommandDelivery(ctx, cmd)
}
func (s *postgresAPIStore) PrepareAgentCommandDelivery(ctx context.Context, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(postgresCommandStorage{store: s}).PrepareAgentCommandDelivery(ctx, cmd)
}
