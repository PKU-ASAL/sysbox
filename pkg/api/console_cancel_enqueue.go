package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func enqueueConsoleCancellation(ctx context.Context, store apiStore, cmd controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	var storage persistence.CommandStorage
	switch s := store.(type) {
	case *localAPIStore:
		storage = localCommandStorage{store: s}
	case *sqliteAPIStore:
		storage = sqliteCommandStorage{store: s}
	case *postgresAPIStore:
		storage = postgresCommandStorage{store: s}
	default:
		return nil, persistence.ErrUnsupportedVersion
	}
	return persistence.NewCommandRepository(storage).Enqueue(ctx, cmd)
}
