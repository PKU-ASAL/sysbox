package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// LifecycleDeletionReceipt is retry evidence, never execution authority.
// Readers that do not know this optional field fail the old-UID CAS rather
// than accepting a broader deletion. No provider effect is replayed.
type LifecycleDeletionReceipt struct {
	TopologyUID string    `json:"topology_uid"`
	Serial      int64     `json:"serial"`
	DeletedAt   time.Time `json:"deleted_at,omitempty"`
	Empty       bool      `json:"empty,omitempty"`
}

// ApplyQuiescentLifecycleDelete checks persisted stop evidence, maintenance and
// lifecycle CAS together. The caller must hold authority and aggregate locks
// through commit. Nil authority is allowed only when absence was checked under
// that same lock. The transition does not authorize provider deletion.
func ApplyQuiescentLifecycleDelete(raw, authorityRaw []byte, topology string, expectedSerial int64, topologyUID string) ([]byte, error) {
	if !topologyName.MatchString(topology) || expectedSerial < 0 || topologyUID == "" {
		return nil, ErrInvalidRecord
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, err
	}
	if err = validateAggregate(a, topology); err != nil {
		return nil, err
	}
	replay := a.DeletedFrom != nil && a.DeletedFrom.TopologyUID == topologyUID && a.DeletedFrom.Serial == expectedSerial
	if replay && (a.Serial != 0 || len(a.State.Resources) > 0 || len(a.Actions) > 0 || len(a.GCSources) > 0 || len(a.Snapshots) > 0) {
		return nil, ErrConflict
	}
	if !replay && (a.Serial != expectedSerial || a.State.TopologyUID != topologyUID) {
		return nil, ErrConflict
	}
	authority, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return nil, err
	}
	if authority.maintenanceBlocks(topologyUID) || authority.maintenanceBlocks(a.State.TopologyUID) {
		return nil, fmt.Errorf("%w: maintenance prevents metadata deletion", ErrConflict)
	}
	for _, grant := range authority.Grants {
		if (grant.Scope.TopologyUID == topologyUID || grant.Scope.TopologyUID == a.State.TopologyUID) && grant.State != controlplane.GrantStopped {
			return nil, fmt.Errorf("%w: execution %s awaits host stop", ErrConflict, grant.ActionID)
		}
	}
	if replay {
		return append([]byte(nil), raw...), nil
	}
	if len(a.GCSources) > 0 {
		// Only this authority-locked path may release retired domain references.
		// Raw state Delete remains forbidden; retained snapshots and resource
		// references require their own explicit cleanup first.
		if len(a.State.Resources) > 0 || len(a.Snapshots) > 0 || gcPending(a) {
			return nil, ErrConflict
		}
		for _, source := range a.GCSources {
			if !source.Retired || source.Updating || len(source.Manifest.References) > 0 {
				return nil, ErrConflict
			}
		}
		a.GCSources = nil

	}
	// Snapshots are historical copies of state taken before each operation.
	// They are deleted with the metadata and never describe live resources,
	// so a lifecycle whose destroy left no resources is still empty even
	// though it retained snapshots; a later incarnation may reuse the name.
	empty := a.Serial > 0 && len(a.State.Resources) == 0
	if err := resetAggregate(a); err != nil {
		return nil, err
	}
	a.DeletedFrom = &LifecycleDeletionReceipt{TopologyUID: topologyUID, Serial: expectedSerial, DeletedAt: a.UpdatedAt, Empty: empty}
	return json.Marshal(a)
}

// DeleteQuiescentLifecycle is the file adapter boundary. The caller holds the
// authority lock while this method locks, validates and writes the aggregate.
func (r *Repository) DeleteQuiescentLifecycle(ctx context.Context, topology string, expectedSerial int64, topologyUID string, authority []byte) error {
	return r.update(ctx, topology, func(a *aggregate) error {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		next, err := ApplyQuiescentLifecycleDelete(raw, authority, topology, expectedSerial, topologyUID)
		if err != nil {
			return err
		}
		reset, err := decodeAggregate(next)
		if err != nil {
			return err
		}
		*a = *reset
		return nil
	})
}
