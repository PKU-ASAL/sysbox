package controlstore

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Local) CompareAndSwapConsoleSession(ctx context.Context, old, next controlplane.ConsoleSession) error {
	return persistence.NewConsoleRepository(localConsoleStorage{store: s}).CompareAndSwap(ctx, old, next)
}

func (s *SQLite) CompareAndSwapConsoleSession(ctx context.Context, old, next controlplane.ConsoleSession) error {
	return persistence.NewConsoleRepository(sqliteConsoleStorage{store: s}).CompareAndSwap(ctx, old, next)
}

func (s *Postgres) CompareAndSwapConsoleSession(ctx context.Context, old, next controlplane.ConsoleSession) error {
	return persistence.NewConsoleRepository(postgresConsoleStorage{store: s}).CompareAndSwap(ctx, old, next)
}
