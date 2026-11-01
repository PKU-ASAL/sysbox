package api

import (
	"context"
	"errors"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Publication can precede authority/dispatch writes on the filesystem. Resume
// those writes from persisted session intent, independently of worker events.
func (s *AgentService) reconcileConsoleAuthority(ctx context.Context) error {
	sessions, err := s.store.ListConsoleSessions(ctx, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, sess := range sessions {
		if sess.CommandID == "" {
			continue
		}
		if sess.Status == controlplane.ConsoleSessionStatusLost || sess.Status == controlplane.ConsoleSessionStatusCancelling {
			if err = s.store.CompareAndSwapConsoleSession(ctx, sess, sess); err != nil {
				failures = append(failures, err)
				continue
			}
			current, readErr := s.store.GetConsoleSession(ctx, sess.ID)
			if readErr != nil {
				failures = append(failures, readErr)
				continue
			}
			sess = *current
		}
		if err = retireConsoleDeliveries(ctx, s.store, sess); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func retireConsoleDeliveries(ctx context.Context, store controlstore.Store, sess controlplane.ConsoleSession) error {
	if (!sess.NotStarted && (!sess.TerminationConfirmed || sess.HostCompletion == "")) || sess.CommandID == "" {
		return nil
	}
	cancellation := persistence.ConsoleCancellationCommand(sess)
	for _, id := range []string{sess.CommandID, cancellation.ID} {
		r, err := readFailureCommand(ctx, store, id)
		if err != nil {
			return err
		}
		if r == nil || r.Retired {
			continue
		}
		cmd := r.Command
		if cmd.AgentID != sess.AgentID || cmd.Session == nil {
			return persistence.ErrConflict
		}
		if id == sess.CommandID {
			if cmd.Type != "session_open" || persistence.ValidateConsoleSessionCAS(*cmd.Session, sess) != nil {
				return persistence.ErrConflict
			}
		} else if cmd.Type != "cancel_command" || !persistence.SameConsoleSession(*cmd.Session, *cancellation.Session) {
			return persistence.ErrConflict
		}
		if err = store.DeleteAgentCommand(ctx, sess.AgentID, id); err != nil {
			return err
		}
	}
	return nil
}
