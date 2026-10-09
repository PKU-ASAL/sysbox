package persistence

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// ConsoleSessionStorage serializes read/transform/write with maintenance
// authority. A nil replacement is a read-only retry. Storage must check
// ConsoleSessionNeedsAdmission while holding authority through publication.
// A non-nil dispatch is the initial command and must commit atomically with
// the new session; collision must leave both registries unchanged.
type ConsoleSessionStorage interface {
	UpdateConsoleSession(context.Context, string, *controlplane.AgentCommand, func(*controlplane.ConsoleSession) (*controlplane.ConsoleSession, error)) error
}

type ConsoleRepository struct{ storage ConsoleSessionStorage }

func NewConsoleRepository(storage ConsoleSessionStorage) *ConsoleRepository {
	return &ConsoleRepository{storage: storage}
}

func (r *ConsoleRepository) Create(ctx context.Context, sess controlplane.ConsoleSession) error {
	if _, err := EncodeConsoleSession(sess); err != nil {
		return err
	}
	return r.storage.UpdateConsoleSession(ctx, sess.ID, nil, func(current *controlplane.ConsoleSession) (*controlplane.ConsoleSession, error) {
		if current == nil {
			return &sess, nil
		}
		if SameConsoleSession(*current, sess) {
			return nil, nil
		}
		return nil, ErrConflict
	})
}

func (r *ConsoleRepository) CompareAndSwap(ctx context.Context, old, next controlplane.ConsoleSession) error {
	if err := ValidateConsoleSessionCAS(old, next); err != nil {
		return err
	}
	if _, err := EncodeConsoleSession(next); err != nil {
		return err
	}
	return r.storage.UpdateConsoleSession(ctx, old.ID, nil, func(current *controlplane.ConsoleSession) (*controlplane.ConsoleSession, error) {
		if current == nil || !SameConsoleSession(*current, old) {
			return nil, ErrConflict
		}
		if SameConsoleSession(*current, next) {
			return nil, nil
		}
		return &next, nil
	})
}

// ConsoleSessionUnresolved describes persisted completion evidence, not proof
// of host quiescence. In particular, lost plus EndedAt is only transport loss.
func ConsoleSessionUnresolved(sess controlplane.ConsoleSession) bool {
	if sess.CreatedAt.IsZero() || sess.EndedAt.IsZero() || sess.EndedAt.Before(sess.CreatedAt) {
		return true
	}
	switch sess.Status {
	case controlplane.ConsoleSessionStatusClosed, controlplane.ConsoleSessionStatusFailed, controlplane.ConsoleSessionStatusCancelled, controlplane.ConsoleSessionStatusDenied:
		return false
	default:
		return true
	}
}

func ConsoleSessionNeedsAdmission(old *controlplane.ConsoleSession, next controlplane.ConsoleSession) bool {
	if old == nil {
		return true
	}
	if SameConsoleSession(*old, next) || !ConsoleSessionUnresolved(next) {
		return false
	}
	if ConsoleSessionUnresolved(*old) {
		switch next.Status {
		case controlplane.ConsoleSessionStatusCancelling, controlplane.ConsoleSessionStatusLost:
			return false
		}
	}
	return true
}
