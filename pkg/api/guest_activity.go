package api

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func guestActivityBeforeReceipt(activities []persistence.GuestActivity, receipt *controlplane.CleanupReceipt) error {
	for _, activity := range activities {
		if activity.Active() || activity.CreatedAt.IsZero() || activity.EndedAt.IsZero() || !activity.CreatedAt.Before(receipt.QueuedAt) || !activity.EndedAt.Before(receipt.QueuedAt) {
			return fmt.Errorf("%w: guest operation invalidates cleanup receipt", persistence.ErrConflict)
		}
	}
	return nil
}

func (s *localAPIStore) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return r.ListGuestActivity(ctx, topology)
}
func (s *sqliteAPIStore) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return r.ListGuestActivity(ctx, topology)
}
func (s *postgresAPIStore) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return r.ListGuestActivity(ctx, topology)
}
