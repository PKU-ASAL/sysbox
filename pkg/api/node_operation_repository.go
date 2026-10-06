package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *localAPIStore) nodeOperationRepository(ctx context.Context) (*persistence.NodeOperationRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return persistence.NewFilesystemNodeOperationRepository(s.runsDir), nil
}
func (s *sqliteAPIStore) nodeOperationRepository(ctx context.Context) (*persistence.NodeOperationRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	return persistence.NewSQLiteNodeOperationRepository(db), nil
}
func (s *postgresAPIStore) nodeOperationRepository(ctx context.Context) (*persistence.NodeOperationRepository, error) {
	pool, err := s.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	return persistence.NewPostgresNodeOperationRepository(pool), nil
}
