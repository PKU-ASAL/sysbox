package api

import (
	"context"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *localAPIStore) CreateConsoleDispatch(ctx context.Context, sess controlplane.ConsoleSession, cmd controlplane.AgentCommand) error {
	return persistence.NewConsoleRepository(localConsoleStorage{store: s}).Admit(ctx, sess, cmd)
}

func (s *sqliteAPIStore) CreateConsoleDispatch(ctx context.Context, sess controlplane.ConsoleSession, cmd controlplane.AgentCommand) error {
	return persistence.NewConsoleRepository(sqliteConsoleStorage{store: s}).Admit(ctx, sess, cmd)
}

func (s *postgresAPIStore) CreateConsoleDispatch(ctx context.Context, sess controlplane.ConsoleSession, cmd controlplane.AgentCommand) error {
	return persistence.NewConsoleRepository(postgresConsoleStorage{store: s}).Admit(ctx, sess, cmd)
}

func (s *localAPIStore) consoleDispatchSeeds(ctx context.Context) ([]persistence.CommandRecord, error) {
	paths, err := localRecordPaths(filepath.Join(s.runsDir, "*", "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	var seeds []persistence.CommandRecord
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		seed, err := persistence.ConsoleDispatchSeed(raw)
		if err != nil {
			return nil, err
		}
		if seed != nil {
			sess := seed.Command.Session
			workspace := sess.Workspace
			if workspace == "" {
				workspace = sess.Topology
			}
			if filepath.Base(path) != sess.ID+".json" || filepath.Base(filepath.Dir(filepath.Dir(path))) != workspace {
				return nil, persistence.ErrCorruptRecord
			}
			seeds = append(seeds, *seed)
		}
	}
	return seeds, nil
}
