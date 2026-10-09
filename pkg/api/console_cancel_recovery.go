package api

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// A persisted cancelling session is the durable intent. Repair only its stable
// outbox command; transport loss alone never requests cancellation. Retirement
// of that delivery does not resolve the original execution's unknown outcome.
func (s *AgentService) reconcileConsoleCancellations(ctx context.Context) error {
	sessions, err := s.store.ListConsoleSessions(ctx, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, sess := range sessions {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if sess.Status != controlplane.ConsoleSessionStatusCancelling {
			continue
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = s.reconcileConsoleCancellation(call, sess)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("console cancellation %s: %w", sess.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *AgentService) reconcileConsoleCancellation(ctx context.Context, sess controlplane.ConsoleSession) error {
	if sess.CreatedAt.IsZero() || sess.Node == "" || validatePathSegment(sess.ID, "session") != nil || validatePathSegment(sess.AgentID, "agent") != nil || validatePathSegment(sess.Topology, "topology") != nil {
		return persistence.ErrInvalidRecord
	}
	cmd := consoleCancellationCommand(sess)
	prior, err := readFailureCommand(ctx, s.store, cmd.ID)
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return err
	}
	if prior != nil {
		if prior.Command.ID != cmd.ID || prior.Command.Type != cmd.Type || prior.Command.AgentID != cmd.AgentID {
			return persistence.ErrConflict
		}
		// Explicit delivery retirement may scrub Session. The stable ID still
		// reserves this intent; do not recreate it or infer execution success.
		if prior.Retired && prior.Command.Session == nil {
			return nil
		}
		if !reflect.DeepEqual(prior.Command.Session, cmd.Session) {
			return persistence.ErrConflict
		}
		// Pending work is delivered by the ordinary outbox. Neither a live
		// lease nor a retired tombstone is reset by reconciliation.
		return nil
	}
	_, err = enqueueConsoleCancellation(ctx, s.store, cmd)
	return err
}
