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

func guestActivityWithAuthority(ctx context.Context, store apiStore, repo *persistence.GuestRepository, topology string) ([]persistence.GuestActivity, error) {
	items, err := repo.ListGuestActivity(ctx, topology)
	if err != nil {
		return nil, err
	}
	grants, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).ListActiveGrants(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		for _, g := range grants {
			if g.ActionID == items[i].ID && g.AgentID == items[i].AgentID {
				items[i].Status = "awaiting_host_stop"
				break
			}
		}
	}
	return items, nil
}

func (s *localAPIStore) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return guestActivityWithAuthority(ctx, s, r, topology)
}
func (s *sqliteAPIStore) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return guestActivityWithAuthority(ctx, s, r, topology)
}
func (s *postgresAPIStore) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return guestActivityWithAuthority(ctx, s, r, topology)
}
