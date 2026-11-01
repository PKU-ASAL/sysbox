package controlstore

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) RegisterAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().RegisterAgent(ctx, a)
}

func (s *SQLite) RegisterAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().RegisterAgent(ctx, a)
}

func (s *Postgres) RegisterAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().RegisterAgent(ctx, a)
}

func (s *Local) agentRepository() *persistence.AgentRepository {
	return persistence.NewAgentRepository(LocalAgentStorage{Store: s})
}

func (s *Local) SaveAgent(ctx context.Context, a controlplane.Agent) error {
	return s.agentRepository().SaveAgent(ctx, a)
}

func (s *Local) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	return s.agentRepository().GetAgent(ctx, id)
}

func (s *Local) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	return s.agentRepository().ListAgents(ctx)
}

func (s *Local) HeartbeatAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().HeartbeatAgent(ctx, a)
}

func (s *Local) UpdateAgentPolicy(ctx context.Context, id string, p persistence.AgentPolicy) (*controlplane.Agent, error) {
	return s.agentRepository().UpdateAgentPolicy(ctx, id, p)
}

func (s *Local) ExpireAgentHeartbeat(ctx context.Context, id string, now time.Time, ttl time.Duration) (*controlplane.Agent, error) {
	return s.agentRepository().ExpireAgentHeartbeat(ctx, id, now, ttl)
}

func (s *SQLite) agentRepository() *persistence.AgentRepository {
	return persistence.NewAgentRepository(sqliteAgentStorage{store: s})
}

func (s *SQLite) SaveAgent(ctx context.Context, a controlplane.Agent) error {
	return s.agentRepository().SaveAgent(ctx, a)
}

func (s *SQLite) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	return s.agentRepository().GetAgent(ctx, id)
}

func (s *SQLite) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	return s.agentRepository().ListAgents(ctx)
}

func (s *SQLite) HeartbeatAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().HeartbeatAgent(ctx, a)
}

func (s *SQLite) UpdateAgentPolicy(ctx context.Context, id string, p persistence.AgentPolicy) (*controlplane.Agent, error) {
	return s.agentRepository().UpdateAgentPolicy(ctx, id, p)
}

func (s *SQLite) ExpireAgentHeartbeat(ctx context.Context, id string, now time.Time, ttl time.Duration) (*controlplane.Agent, error) {
	return s.agentRepository().ExpireAgentHeartbeat(ctx, id, now, ttl)
}

func (s *Postgres) agentRepository() *persistence.AgentRepository {
	return persistence.NewAgentRepository(postgresAgentStorage{store: s})
}

func (s *Postgres) SaveAgent(ctx context.Context, a controlplane.Agent) error {
	return s.agentRepository().SaveAgent(ctx, a)
}

func (s *Postgres) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	return s.agentRepository().GetAgent(ctx, id)
}

func (s *Postgres) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	return s.agentRepository().ListAgents(ctx)
}

func (s *Postgres) HeartbeatAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().HeartbeatAgent(ctx, a)
}

func (s *Postgres) UpdateAgentPolicy(ctx context.Context, id string, p persistence.AgentPolicy) (*controlplane.Agent, error) {
	return s.agentRepository().UpdateAgentPolicy(ctx, id, p)
}

func (s *Postgres) ExpireAgentHeartbeat(ctx context.Context, id string, now time.Time, ttl time.Duration) (*controlplane.Agent, error) {
	return s.agentRepository().ExpireAgentHeartbeat(ctx, id, now, ttl)
}
