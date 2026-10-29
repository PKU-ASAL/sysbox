package controlstore

import (
	"context"

	"github.com/oslab/sysbox/pkg/persistence"
)

func guestActivityWithAuthority(ctx context.Context, store Store, repo *persistence.GuestRepository, topology string) ([]persistence.GuestActivity, error) {
	items, err := repo.ListGuestActivity(ctx, topology)
	if err != nil {
		return nil, err
	}
	grants, err := persistence.NewExecutionGrantRepository(AuthorityStorage{Store: store}).ListActiveGrants(ctx)
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

func (s *Local) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return guestActivityWithAuthority(ctx, s, r, topology)
}

func (s *SQLite) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return guestActivityWithAuthority(ctx, s, r, topology)
}

func (s *Postgres) ListGuestActivity(ctx context.Context, topology string) ([]persistence.GuestActivity, error) {
	r, err := s.guestRepository(ctx)
	if err != nil {
		return nil, err
	}
	return guestActivityWithAuthority(ctx, s, r, topology)
}
