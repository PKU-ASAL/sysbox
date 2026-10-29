package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Resume only durable terminal command outcomes; never reissue session_open or
// infer a result from an expired lease, a retired delivery alone, or disconnect.
func (s *AgentService) reconcileConsoleCompletions(ctx context.Context) error {
	if err := s.reconcileConsoleAuthority(ctx); err != nil {
		return err
	}
	return s.reconcileConsoleCompletion(ctx, "")
}

func (s *AgentService) reconcileConsoleCompletion(ctx context.Context, sessionID string) error {
	var storage persistence.CommandStorage
	switch store := s.store.(type) {
	case *localAPIStore:
		storage = localCommandStorage{store: store}
	case *sqliteAPIStore:
		storage = sqliteCommandStorage{store: store}
	case *postgresAPIStore:
		storage = postgresCommandStorage{store: store}
	default:
		return persistence.ErrUnsupportedVersion
	}
	if s.consoles == nil {
		return fmt.Errorf("console registry unavailable")
	}
	records, err := storage.ListCommands(ctx, "")
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, r := range records {
		if r.Command.Type == "session_open" && r.Command.Session != nil {
			counts[r.Command.Session.ID]++
		}
	}
	var failures []error
	for _, r := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		cmd := r.Command
		if !r.Retired || !cmd.IsTerminal() || cmd.EndedAt.IsZero() || cmd.Type != "session_open" || cmd.Session == nil {
			continue
		}
		if sessionID != "" && cmd.Session.ID != sessionID {
			continue
		}
		sess, err := s.consoles.Snapshot(cmd.Session.ID)
		if err == nil && (sess.ID == "" || sess.CreatedAt.IsZero() || cmd.EndedAt.Before(sess.CreatedAt) || counts[sess.ID] != 1 || sess.AgentID != cmd.AgentID || sess.Topology != cmd.Session.Topology || sess.Node != cmd.Session.Node || !sess.CreatedAt.Equal(cmd.Session.CreatedAt)) {
			err = persistence.ErrConflict
		}
		if err == nil {
			if !persistence.ConsoleSessionUnresolved(sess) {
				continue
			}
			err = s.updateConsoleSessionFromCommandEvent(cmd, controlplane.AgentCommandEvent{Status: cmd.Status, Error: cmd.Err, CreatedAt: cmd.EndedAt})
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("console completion %s: %w", cmd.ID, err))
		}
	}
	return errors.Join(failures...)
}
