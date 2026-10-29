package controlstore

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) guestRepository(ctx context.Context) (*persistence.GuestRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return persistence.NewFilesystemGuestRepository(s.RunsDir), nil
}

func (s *SQLite) guestRepository(ctx context.Context) (*persistence.GuestRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := s.OpenDB()
	if err != nil {
		return nil, err
	}
	return persistence.NewSQLiteGuestRepository(db), nil
}

func (s *Postgres) guestRepository(ctx context.Context) (*persistence.GuestRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pool, err := s.PoolFor(ctx)
	if err != nil {
		return nil, err
	}
	return persistence.NewPostgresGuestRepository(pool), nil
}

func (s *Local) SaveGuestExecution(ctx context.Context, x controlplane.GuestExecution) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestExecution(ctx, x)
}

func (s *Local) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestExecution(ctx, id)
}

func (s *Local) CompareAndSwapGuestExecution(ctx context.Context, x controlplane.GuestExecution, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestExecution(ctx, x, expected)
}

func (s *Local) LatestGuestAcceptance(ctx context.Context, id string) (time.Time, time.Time, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return repo.LatestGuestAcceptance(ctx, id)
}

func (s *SQLite) SaveGuestExecution(ctx context.Context, x controlplane.GuestExecution) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestExecution(ctx, x)
}

func (s *SQLite) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestExecution(ctx, id)
}

func (s *SQLite) CompareAndSwapGuestExecution(ctx context.Context, x controlplane.GuestExecution, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestExecution(ctx, x, expected)
}

func (s *SQLite) LatestGuestAcceptance(ctx context.Context, id string) (time.Time, time.Time, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return repo.LatestGuestAcceptance(ctx, id)
}

func (s *Postgres) SaveGuestExecution(ctx context.Context, x controlplane.GuestExecution) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestExecution(ctx, x)
}

func (s *Postgres) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestExecution(ctx, id)
}

func (s *Postgres) CompareAndSwapGuestExecution(ctx context.Context, x controlplane.GuestExecution, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestExecution(ctx, x, expected)
}

func (s *Postgres) LatestGuestAcceptance(ctx context.Context, id string) (time.Time, time.Time, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return repo.LatestGuestAcceptance(ctx, id)
}
