package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

var errGuestFileOperationStoreNotFound = persistence.ErrNotFound

func (s *localAPIStore) SaveGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestFileOperation(ctx, x)
}
func (s *localAPIStore) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestFileOperation(ctx, id)
}
func (s *localAPIStore) CompareAndSwapGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestFileOperation(ctx, x, expected)
}

func (s *sqliteAPIStore) SaveGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestFileOperation(ctx, x)
}
func (s *sqliteAPIStore) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestFileOperation(ctx, id)
}
func (s *sqliteAPIStore) CompareAndSwapGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestFileOperation(ctx, x, expected)
}

func (s *postgresAPIStore) SaveGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation) error {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return err
	}
	return repo.SaveGuestFileOperation(ctx, x)
}
func (s *postgresAPIStore) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repo.GetGuestFileOperation(ctx, id)
}
func (s *postgresAPIStore) CompareAndSwapGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation, expected int64) (bool, error) {
	repo, err := s.guestRepository(ctx)
	if err != nil {
		return false, err
	}
	return repo.CompareAndSwapGuestFileOperation(ctx, x, expected)
}
