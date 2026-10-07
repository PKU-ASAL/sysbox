package api

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func saveInventoryObservation(ctx context.Context, store apiStore, inv controlplane.AgentInventory) error {
	if inv.SessionID == "" || inv.Sequence == 0 {
		return persistence.ErrInvalidRecord
	}
	raw, err := persistence.EncodeInventory(inv)
	if err != nil {
		return err
	}
	return persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).PublishObservation(ctx, inv.AgentID, inv.SessionID, "inventory", inv.Sequence, raw)
}
func loadInventoryObservation(ctx context.Context, store apiStore, id string) (*controlplane.AgentInventory, error) {
	record, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).ReadObservation(ctx, id, "inventory")
	if err != nil {
		return nil, err
	}
	inv, err := persistence.DecodeInventory(record.Data)
	if err != nil {
		return nil, err
	}
	if inv.AgentID != record.AgentID || inv.SessionID != record.SessionID || inv.Sequence != record.Sequence {
		return nil, persistence.ErrCorruptRecord
	}
	return inv, nil
}
func saveProjectionObservation(ctx context.Context, store apiStore, p controlplane.ResourceProjection) error {
	if p.AgentID == "" || p.SessionID == "" || p.Sequence == 0 {
		return persistence.ErrInvalidRecord
	}
	raw, err := persistence.EncodeProjection(p)
	if err != nil {
		return err
	}
	return persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).PublishObservation(ctx, p.AgentID, p.SessionID, "projection/"+p.Topology, p.Sequence, raw)
}
func loadProjectionObservation(ctx context.Context, store apiStore, topology string) (*controlplane.ResourceProjection, error) {
	records, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).ListObservations(ctx, "projection/"+topology)
	if err != nil {
		return nil, err
	}
	owner := ""
	if placements, ok := store.(durablePlacementStore); ok {
		placement, err := placements.GetTopologyPlacement(ctx, topology)
		if err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return nil, err
		}
		if placement != nil {
			owner = placement.AgentID
		}
	}
	var result *controlplane.ResourceProjection
	for _, record := range records {
		if owner != "" && record.AgentID != owner {
			continue
		}
		p, err := persistence.DecodeProjection(record.Data)
		if err != nil {
			return nil, err
		}
		if p.Topology != topology || p.AgentID != record.AgentID || p.SessionID != record.SessionID || p.Sequence != record.Sequence {
			return nil, persistence.ErrCorruptRecord
		}
		if result != nil {
			return nil, persistence.ErrConflict
		}
		result = p
	}
	return result, nil
}

func (s *localAPIStore) SaveAgentInventory(ctx context.Context, x controlplane.AgentInventory) error {
	return saveInventoryObservation(ctx, s, x)
}
func (s *sqliteAPIStore) SaveAgentInventory(ctx context.Context, x controlplane.AgentInventory) error {
	return saveInventoryObservation(ctx, s, x)
}
func (s *postgresAPIStore) SaveAgentInventory(ctx context.Context, x controlplane.AgentInventory) error {
	return saveInventoryObservation(ctx, s, x)
}
func (s *localAPIStore) GetAgentInventory(ctx context.Context, id string) (*controlplane.AgentInventory, error) {
	return loadInventoryObservation(ctx, s, id)
}
func (s *sqliteAPIStore) GetAgentInventory(ctx context.Context, id string) (*controlplane.AgentInventory, error) {
	return loadInventoryObservation(ctx, s, id)
}
func (s *postgresAPIStore) GetAgentInventory(ctx context.Context, id string) (*controlplane.AgentInventory, error) {
	return loadInventoryObservation(ctx, s, id)
}
func (s *localAPIStore) SaveResourceProjection(ctx context.Context, x controlplane.ResourceProjection) error {
	return saveProjectionObservation(ctx, s, x)
}
func (s *sqliteAPIStore) SaveResourceProjection(ctx context.Context, x controlplane.ResourceProjection) error {
	return saveProjectionObservation(ctx, s, x)
}
func (s *postgresAPIStore) SaveResourceProjection(ctx context.Context, x controlplane.ResourceProjection) error {
	return saveProjectionObservation(ctx, s, x)
}
func (s *localAPIStore) LoadResourceProjection(ctx context.Context, id string) (*controlplane.ResourceProjection, error) {
	return loadProjectionObservation(ctx, s, id)
}
func (s *sqliteAPIStore) LoadResourceProjection(ctx context.Context, id string) (*controlplane.ResourceProjection, error) {
	return loadProjectionObservation(ctx, s, id)
}
func (s *postgresAPIStore) LoadResourceProjection(ctx context.Context, id string) (*controlplane.ResourceProjection, error) {
	return loadProjectionObservation(ctx, s, id)
}
