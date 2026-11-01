package controlstore

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *Local) SaveGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestFileOperation(ctx, x)
}

func (s *Local) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestFileOperation(ctx, id)
}

func (s *Local) CompareAndSwapGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestFileOperation(ctx, x, expected)
}

func (s *SQLite) SaveGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestFileOperation(ctx, x)
}

func (s *SQLite) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestFileOperation(ctx, id)
}

func (s *SQLite) CompareAndSwapGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestFileOperation(ctx, x, expected)
}

func (s *Postgres) SaveGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestFileOperation(ctx, x)
}

func (s *Postgres) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestFileOperation(ctx, id)
}

func (s *Postgres) CompareAndSwapGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestFileOperation(ctx, x, expected)
}
