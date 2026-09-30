package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

func (s *localAPIStore) RegisterAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().RegisterAgent(ctx, a)
}
func (s *sqliteAPIStore) RegisterAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().RegisterAgent(ctx, a)
}
func (s *postgresAPIStore) RegisterAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().RegisterAgent(ctx, a)
}

func (s *localAPIStore) agentRepository() *persistence.AgentRepository {
	return persistence.NewAgentRepository(localAgentStorage{store: s})
}
func (s *localAPIStore) SaveAgent(ctx context.Context, a controlplane.Agent) error {
	return s.agentRepository().SaveAgent(ctx, a)
}
func (s *localAPIStore) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	return s.agentRepository().GetAgent(ctx, id)
}
func (s *localAPIStore) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	return s.agentRepository().ListAgents(ctx)
}
func (s *localAPIStore) HeartbeatAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().HeartbeatAgent(ctx, a)
}
func (s *localAPIStore) UpdateAgentPolicy(ctx context.Context, id string, p persistence.AgentPolicy) (*controlplane.Agent, error) {
	return s.agentRepository().UpdateAgentPolicy(ctx, id, p)
}
func (s *localAPIStore) ExpireAgentHeartbeat(ctx context.Context, id string, now time.Time, ttl time.Duration) (*controlplane.Agent, error) {
	return s.agentRepository().ExpireAgentHeartbeat(ctx, id, now, ttl)
}
func (s *sqliteAPIStore) agentRepository() *persistence.AgentRepository {
	return persistence.NewAgentRepository(sqliteAgentStorage{store: s})
}
func (s *sqliteAPIStore) SaveAgent(ctx context.Context, a controlplane.Agent) error {
	return s.agentRepository().SaveAgent(ctx, a)
}
func (s *sqliteAPIStore) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	return s.agentRepository().GetAgent(ctx, id)
}
func (s *sqliteAPIStore) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	return s.agentRepository().ListAgents(ctx)
}
func (s *sqliteAPIStore) HeartbeatAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().HeartbeatAgent(ctx, a)
}
func (s *sqliteAPIStore) UpdateAgentPolicy(ctx context.Context, id string, p persistence.AgentPolicy) (*controlplane.Agent, error) {
	return s.agentRepository().UpdateAgentPolicy(ctx, id, p)
}
func (s *sqliteAPIStore) ExpireAgentHeartbeat(ctx context.Context, id string, now time.Time, ttl time.Duration) (*controlplane.Agent, error) {
	return s.agentRepository().ExpireAgentHeartbeat(ctx, id, now, ttl)
}
func (s *postgresAPIStore) agentRepository() *persistence.AgentRepository {
	return persistence.NewAgentRepository(postgresAgentStorage{store: s})
}
func (s *postgresAPIStore) SaveAgent(ctx context.Context, a controlplane.Agent) error {
	return s.agentRepository().SaveAgent(ctx, a)
}
func (s *postgresAPIStore) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	return s.agentRepository().GetAgent(ctx, id)
}
func (s *postgresAPIStore) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	return s.agentRepository().ListAgents(ctx)
}
func (s *postgresAPIStore) HeartbeatAgent(ctx context.Context, a controlplane.Agent) (*controlplane.Agent, error) {
	return s.agentRepository().HeartbeatAgent(ctx, a)
}
func (s *postgresAPIStore) UpdateAgentPolicy(ctx context.Context, id string, p persistence.AgentPolicy) (*controlplane.Agent, error) {
	return s.agentRepository().UpdateAgentPolicy(ctx, id, p)
}
func (s *postgresAPIStore) ExpireAgentHeartbeat(ctx context.Context, id string, now time.Time, ttl time.Duration) (*controlplane.Agent, error) {
	return s.agentRepository().ExpireAgentHeartbeat(ctx, id, now, ttl)
}
