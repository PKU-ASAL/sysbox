package persistence

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type agentStorageFixture struct {
	current *controlplane.Agent
	failure error
}

func (s *agentStorageFixture) ReadAgent(context.Context, string) (*controlplane.Agent, error) {
	return s.current, nil
}
func (s *agentStorageFixture) ListAgents(context.Context) ([]controlplane.Agent, error) {
	if s.current == nil {
		return nil, nil
	}
	return []controlplane.Agent{*s.current}, nil
}
func (s *agentStorageFixture) UpdateAgent(_ context.Context, _ string, change func(*controlplane.Agent) (*controlplane.Agent, error)) (*controlplane.Agent, error) {
	next, err := change(s.current)
	if err != nil {
		return nil, err
	}
	if s.failure != nil {
		return nil, s.failure
	}
	if next != nil {
		s.current = next
	}
	return s.current, nil
}
func TestAgentRepositoryFailedCommitCannotPublishNewState(t *testing.T) {
	for _, operation := range []string{"save", "register", "heartbeat", "policy", "offline"} {
		t.Run(operation, func(t *testing.T) {
			current := controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, LastHeartbeat: time.Now().Add(-time.Hour)}
			failure := errors.New("commit failed")
			storage := &agentStorageFixture{current: &current, failure: failure}
			repo := NewAgentRepository(storage)
			ctx := context.Background()
			var next *controlplane.Agent
			var err error
			yes := true
			switch operation {
			case "save":
				err = repo.SaveAgent(ctx, controlplane.Agent{ID: "agent", Version: "replacement"})
			case "register":
				next, err = repo.RegisterAgent(ctx, controlplane.Agent{ID: "agent", Version: "replacement"})
			case "heartbeat":
				next, err = repo.HeartbeatAgent(ctx, controlplane.Agent{ID: "agent", Version: "replacement"})
			case "policy":
				next, err = repo.UpdateAgentPolicy(ctx, "agent", AgentPolicy{Disabled: &yes})
			case "offline":
				next, err = repo.ExpireAgentHeartbeat(ctx, "agent", time.Now(), time.Minute)
			}
			require.ErrorIs(t, err, failure)
			require.Nil(t, next)
			require.False(t, storage.current.Disabled)
			require.Equal(t, controlplane.AgentStatusOnline, storage.current.Status)
			require.Empty(t, storage.current.Version)
		})
	}
}
func TestAgentHeartbeatUsesCommitTimeAndPreservesCreation(t *testing.T) {
	now := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	created := now.Add(-time.Hour)
	storage := &agentStorageFixture{current: &controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, CreatedAt: created}}
	repo := NewAgentRepository(storage)
	repo.now = func() time.Time { return now }
	got, err := repo.HeartbeatAgent(context.Background(), controlplane.Agent{ID: "agent", CreatedAt: now.Add(time.Hour), LastHeartbeat: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour)})
	require.NoError(t, err)
	require.True(t, created.Equal(got.CreatedAt))
	require.True(t, now.Equal(got.LastHeartbeat))
	require.True(t, now.Equal(got.UpdatedAt))
}
