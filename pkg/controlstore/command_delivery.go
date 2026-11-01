package controlstore

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) GetRunRecoveryReport(ctx context.Context, agent, id string) (*controlplane.RunRecoveryReport, error) {
	return persistence.NewCommandRepository(LocalCommandStorage{Store: s}).GetRunRecoveryReport(ctx, agent, id)
}

func (s *SQLite) GetRunRecoveryReport(ctx context.Context, agent, id string) (*controlplane.RunRecoveryReport, error) {
	return persistence.NewCommandRepository(SqliteCommandStorage{Store: s}).GetRunRecoveryReport(ctx, agent, id)
}

func (s *Postgres) GetRunRecoveryReport(ctx context.Context, agent, id string) (*controlplane.RunRecoveryReport, error) {
	return persistence.NewCommandRepository(PostgresCommandStorage{Store: s}).GetRunRecoveryReport(ctx, agent, id)
}

func (s *Local) ApplyAgentCommandEvent(ctx context.Context, e controlplane.AgentCommandEvent) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(LocalCommandStorage{Store: s}).ApplyAgentCommandEvent(ctx, e)
}

func (s *SQLite) ApplyAgentCommandEvent(ctx context.Context, e controlplane.AgentCommandEvent) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(SqliteCommandStorage{Store: s}).ApplyAgentCommandEvent(ctx, e)
}

func (s *Postgres) ApplyAgentCommandEvent(ctx context.Context, e controlplane.AgentCommandEvent) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(PostgresCommandStorage{Store: s}).ApplyAgentCommandEvent(ctx, e)
}

func (s *Local) PrepareAgentCommandDelivery(ctx context.Context, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(LocalCommandStorage{Store: s}).PrepareAgentCommandDelivery(ctx, cmd)
}

func (s *SQLite) PrepareAgentCommandDelivery(ctx context.Context, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(SqliteCommandStorage{Store: s}).PrepareAgentCommandDelivery(ctx, cmd)
}

func (s *Postgres) PrepareAgentCommandDelivery(ctx context.Context, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	return persistence.NewCommandRepository(PostgresCommandStorage{Store: s}).PrepareAgentCommandDelivery(ctx, cmd)
}
