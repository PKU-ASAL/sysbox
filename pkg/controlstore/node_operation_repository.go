package controlstore

import (
	"context"

	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) nodeOperationRepository(ctx context.Context) (*persistence.NodeOperationRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return persistence.NewFilesystemNodeOperationRepository(s.RunsDir), nil
}

func (s *SQLite) nodeOperationRepository(ctx context.Context) (*persistence.NodeOperationRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := s.OpenDB()
	if err != nil {
		return nil, err
	}
	return persistence.NewSQLiteNodeOperationRepository(db), nil
}

func (s *Postgres) nodeOperationRepository(ctx context.Context) (*persistence.NodeOperationRepository, error) {
	pool, err := s.PoolFor(ctx)
	if err != nil {
		return nil, err
	}
	return persistence.NewPostgresNodeOperationRepository(pool), nil
}
