package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

func (s *localAPIStore) guestRepository(ctx context.Context) (*persistence.GuestRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return persistence.NewFilesystemGuestRepository(s.runsDir), nil
}
func (s *sqliteAPIStore) guestRepository(ctx context.Context) (*persistence.GuestRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	return persistence.NewSQLiteGuestRepository(db), nil
}
func (s *postgresAPIStore) guestRepository(ctx context.Context) (*persistence.GuestRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pool, err := s.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	return persistence.NewPostgresGuestRepository(pool), nil
}

func (s *localAPIStore) SaveGuestExecution(ctx context.Context, x controlplane.GuestExecution) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestExecution(ctx, x)
}
func (s *localAPIStore) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestExecution(ctx, id)
}
func (s *localAPIStore) CompareAndSwapGuestExecution(ctx context.Context, x controlplane.GuestExecution, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestExecution(ctx, x, expected)
}

func (s *localAPIStore) LatestGuestAcceptance(ctx context.Context, id string) (time.Time, time.Time, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return repo.LatestGuestAcceptance(ctx, id)
}

func (s *sqliteAPIStore) SaveGuestExecution(ctx context.Context, x controlplane.GuestExecution) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestExecution(ctx, x)
}
func (s *sqliteAPIStore) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestExecution(ctx, id)
}
func (s *sqliteAPIStore) CompareAndSwapGuestExecution(ctx context.Context, x controlplane.GuestExecution, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestExecution(ctx, x, expected)
}

func (s *sqliteAPIStore) LatestGuestAcceptance(ctx context.Context, id string) (time.Time, time.Time, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return repo.LatestGuestAcceptance(ctx, id)
}

func (s *postgresAPIStore) SaveGuestExecution(ctx context.Context, x controlplane.GuestExecution) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestExecution(ctx, x)
}
func (s *postgresAPIStore) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestExecution(ctx, id)
}
func (s *postgresAPIStore) CompareAndSwapGuestExecution(ctx context.Context, x controlplane.GuestExecution, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestExecution(ctx, x, expected)
}

func (s *postgresAPIStore) LatestGuestAcceptance(ctx context.Context, id string) (time.Time, time.Time, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return repo.LatestGuestAcceptance(ctx, id)
}
