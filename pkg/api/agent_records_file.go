package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"os"
	"path/filepath"
)

type localAgentStorage struct{ store *localAPIStore }

func (b localAgentStorage) ReadAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(b.store.runsDir, "_agents", id, "agent.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	agent, err := persistence.DecodeAgentRecord(raw)
	if err != nil {
		return nil, err
	}
	if agent.ID != id {
		return nil, persistence.ErrCorruptRecord
	}
	return agent, nil
}
func (b localAgentStorage) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	files, err := localRecordPaths(filepath.Join(b.store.runsDir, "_agents", "*", "agent.json"))
	if err != nil {
		return nil, err
	}
	out := make([]controlplane.Agent, 0, len(files))
	for _, path := range files {
		agent, err := b.ReadAgent(ctx, filepath.Base(filepath.Dir(path)))
		if err != nil {
			return nil, err
		}
		if agent == nil {
			return nil, persistence.ErrCorruptRecord
		}
		out = append(out, *agent)
	}
	return out, nil
}
func (b localAgentStorage) UpdateAgent(ctx context.Context, id string, transform func(*controlplane.Agent) (*controlplane.Agent, error)) (*controlplane.Agent, error) {
	unlock, err := b.store.lockRunRecords(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	current, err := b.ReadAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	next, err := transform(current)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return current, nil
	}
	raw, err := persistence.EncodeAgentRecord(*next)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeLocalObject(filepath.Join(b.store.runsDir, "_agents", id, "agent.json"), json.RawMessage(raw)); err != nil {
		return nil, err
	}
	return next, nil
}
