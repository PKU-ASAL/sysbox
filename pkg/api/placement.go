package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type durablePlacementStore interface {
	GetTopologyPlacement(context.Context, string) (*topologyPlacement, error)
	SaveTopologyPlacement(context.Context, topologyPlacement) error
	DeleteTopologyPlacement(context.Context, string) error
}

func (s *WorkspaceService) deletePlacement(ctx context.Context, topology string) error {
	if s.placementStore != nil {
		if err := s.placementStore.DeleteTopologyPlacement(ctx, topology); err != nil {
			return err
		}
	}
	if err := os.Remove(s.placementFile(topology)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// topologyPlacement is the durable host affinity for host-local resources.
type topologyPlacement struct {
	Topology string    `json:"topology"`
	AgentID  string    `json:"agent_id"`
	Protocol string    `json:"protocol"`
	BoundAt  time.Time `json:"bound_at"`
}

func (s *WorkspaceService) placementFile(topology string) string {
	if strings.HasPrefix(s.stateBackend, "sqlite://") {
		root := strings.TrimPrefix(s.stateBackend, "sqlite://") + ".placements"
		return filepath.Join(root, topology+".json")
	}
	return filepath.Join(s.runsDir, topology, "placement.json")
}

func (s *WorkspaceService) loadPlacement(topology string) (*topologyPlacement, error) {
	if s.placementStore != nil {
		return s.placementStore.GetTopologyPlacement(context.Background(), topology)
	}
	data, err := os.ReadFile(s.placementFile(topology))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read topology placement: %w", err)
	}
	var p topologyPlacement
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode topology placement: %w", err)
	}
	if p.Topology != topology || p.AgentID == "" {
		return nil, fmt.Errorf("invalid topology placement")
	}
	return &p, nil
}

func (s *WorkspaceService) bindPlacement(topology, agentID, protocol string) error {
	p := topologyPlacement{Topology: topology, AgentID: agentID, Protocol: protocol, BoundAt: time.Now().UTC()}
	if s.placementStore != nil {
		return s.placementStore.SaveTopologyPlacement(context.Background(), p)
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
	if existing, err := s.loadPlacement(topology); err != nil {
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
