package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlstore"
)

func (s *Catalog) DeletePlacement(ctx context.Context, topology string) error {
	finish, err := s.BeginGCProductionWrite(ctx, topology, "placement")
	if err != nil {
		return err
	}
	if err = s.deletePlacementUnpublished(ctx, topology); err != nil {
		return err
	}
	// Retain historical placement evidence until explicit source retirement.
	return finish(nil)
}

func (s *Catalog) deletePlacementUnpublished(ctx context.Context, topology string) error {
	if s.PlacementStore != nil {
		if err := s.PlacementStore.DeleteTopologyPlacement(ctx, topology); err != nil {
			return err
		}
	}
	if err := os.Remove(s.placementFile(topology)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Catalog) placementFile(topology string) string {
	if strings.HasPrefix(s.stateBackend, "sqlite://") {
		root := strings.TrimPrefix(s.stateBackend, "sqlite://") + ".placements"
		return filepath.Join(root, topology+".json")
	}
	return filepath.Join(s.RunsDir, topology, "placement.json")
}

func (s *Catalog) LoadPlacement(topology string) (*controlstore.Placement, error) {
	return s.LoadPlacementContext(context.Background(), topology)
}

func (s *Catalog) LoadPlacementContext(ctx context.Context, topology string) (*controlstore.Placement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.PlacementStore != nil {
		return s.PlacementStore.GetTopologyPlacement(ctx, topology)
	}
	data, err := os.ReadFile(s.placementFile(topology))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read topology placement: %w", err)
	}
	var p controlstore.Placement
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode topology placement: %w", err)
	}
	if p.Topology != topology || p.AgentID == "" {
		return nil, fmt.Errorf("invalid topology placement")
	}
	return &p, nil
}

func (s *Catalog) BindPlacement(topology, agentID, protocol string) error {
	finish, err := s.BeginGCProductionWrite(context.Background(), topology, "placement")
	if err != nil {
		return err
	}
	if err = s.bindPlacementUnpublished(topology, agentID, protocol); err != nil {
		return err
	}
	return finish(map[string][]string{"placement": {agentID}})
}

func (s *Catalog) bindPlacementUnpublished(topology, agentID, protocol string) error {
	p := controlstore.Placement{Topology: topology, AgentID: agentID, Protocol: protocol, BoundAt: time.Now().UTC()}
	if s.PlacementStore != nil {
		return s.PlacementStore.SaveTopologyPlacement(context.Background(), p)
	}
	path := s.placementFile(topology)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lease := flock.New(path + ".lock")
	lockCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	locked, err := lease.TryLockContext(lockCtx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock topology placement: %w", err)
	}
	if !locked {
		return fmt.Errorf("lock topology placement timed out")
	}
	defer lease.Unlock()
	if existing, err := s.LoadPlacement(topology); err != nil {
		return err
	} else if existing != nil {
		if existing.AgentID != agentID {
			return fmt.Errorf("topology %q is already placed on agent %q", topology, existing.AgentID)
		}
		return nil
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
