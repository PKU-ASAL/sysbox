package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

func (s *AgentService) reconcileGuestHostCompletions(ctx context.Context) error {
	activities, err := s.store.ListGuestActivity(ctx, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, activity := range activities {
		if !activity.CompletionRecorded {
			continue
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.reconcileGuestHostCompletion(call, activity.ID)
		cancel()
		if err != nil && !errors.Is(err, persistence.ErrConflict) {
			failures = append(failures, fmt.Errorf("guest completion %s: %w", activity.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *AgentService) reconcileGuestHostCompletion(ctx context.Context, id string) error {
	_, err := s.store.RecoverGuestExecutionHostCompletion(ctx, id)
	return err
}
