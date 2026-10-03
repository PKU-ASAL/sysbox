package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *localAPIStore) CompleteRun(ctx context.Context, agent, id string, c controlplane.RunCompletion) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(localRunStorage{store: s}).CompleteRun(ctx, agent, id, c)
}
func (s *sqliteAPIStore) CompleteRun(ctx context.Context, agent, id string, c controlplane.RunCompletion) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(sqliteRunStorage{store: s}).CompleteRun(ctx, agent, id, c)
}
func (s *postgresAPIStore) CompleteRun(ctx context.Context, agent, id string, c controlplane.RunCompletion) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(postgresRunStorage{store: s}).CompleteRun(ctx, agent, id, c)
}
