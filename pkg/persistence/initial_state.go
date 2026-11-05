package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// InitializeAuthorizedTopology persists the empty ownership baseline before a
// first Apply can perform provider effects. The caller must hold the topology
// execution lock and establish that this is the first canonical Run, under a
// live host grant binding expectedUID. It must never be used by Destroy, by a
// metadata query, or to repair missing history. CAS on UID prevents a vanished
// aggregate from being silently replaced with a newly generated identity.
func (r *Repository) InitializeAuthorizedTopology(ctx context.Context, topology, expectedUID string) error {
	if expectedUID == "" {
		return ErrInvalidRecord
	}
	return r.InitializeAuthorizedTopologyWithGuard(ctx, topology, expectedUID, nil)
}

// InitializeAuthorizedTopologyWithGuard observes deletion evidence under the
// same lifecycle lock as the initial commit. The caller holds admission/authority locks.
func (r *Repository) InitializeAuthorizedTopologyWithGuard(ctx context.Context, topology, expectedUID string, guard func(*LifecycleDeletionReceipt) error) error {
	return r.update(ctx, topology, func(a *aggregate) error {
		if guard != nil {
			if err := guard(a.DeletedFrom); err != nil {
				return err
			}
		}
		return initializeAuthorizedTopology(a, expectedUID)
	})
}

// ApplyInitialTopology is the SQL transaction adapter; nil/missing lifecycle
// input is never interpreted as a new authorized identity.
func ApplyInitialTopology(raw []byte, topology, expectedUID string) ([]byte, error) {
	return ApplyInitialTopologyWithGuard(raw, topology, expectedUID, nil)
}

func ApplyInitialTopologyWithGuard(raw []byte, topology, expectedUID string, guard func(*LifecycleDeletionReceipt) error) ([]byte, error) {
	if len(raw) == 0 {
		return nil, ErrNotFound
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, err
	}
	if err = validateAggregate(a, topology); err != nil {
		return nil, err
	}
	if guard != nil {
		if err := guard(a.DeletedFrom); err != nil {
			return nil, err
		}
	}
	if err = initializeAuthorizedTopology(a, expectedUID); err != nil {
		return nil, err
	}
	if err = validateAggregate(a, topology); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

func initializeAuthorizedTopology(a *aggregate, expectedUID string) error {
	if a.State.TopologyUID != expectedUID {
		return fmt.Errorf("%w: initial topology ownership changed", ErrConflict)
	}
	if a.Serial > 0 {
		return nil
	}
	if a.DeletedFrom != nil && (!a.DeletedFrom.Empty || a.DeletedFrom.DeletedAt.IsZero()) {
		return fmt.Errorf("%w: deletion did not certify an empty previous lifecycle", ErrConflict)
	}

	if len(a.State.Resources) != 0 || len(a.Actions) != 0 || len(a.Snapshots) != 0 || len(a.Restores) != 0 || len(a.ResetContinuations) != 0 || len(a.GC) != 0 || len(a.IntentDispositions) != 0 || len(a.AdoptionRecoveries) != 0 || len(a.OwnershipTargets) != 0 || len(a.OwnershipRetirements) != 0 {
		return fmt.Errorf("%w: initial topology contains lifecycle history", ErrConflict)
	}
	// Complete metadata-only publication is normal before the first Apply
	// (revision materialization and placement). Retain it, but never turn an
	// incomplete publisher or external managed-resource reference into absence.
	for _, source := range a.GCSources {
		if source.Updating || len(source.Manifest.References) > 0 || !source.Manifest.ValidUntil.After(time.Now()) {
			return fmt.Errorf("%w: initial topology has unverified external resource history", ErrConflict)
		}
	}
	a.Serial = 1
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// ValidateInitialTopologyAuthority prevents an old lifecycle's unfinished
// authority or a second live action from being hidden by an empty baseline.
func ValidateInitialTopologyAuthority(raw []byte, current controlplane.ExecutionGrant, deleted *LifecycleDeletionReceipt) error {
	authority, err := decodeExecutionAuthority(raw)
	if err != nil {
		return err
	}
	for _, grant := range authority.Grants {
		if grant.State == controlplane.GrantStopped {
			continue
		}
		if deleted != nil && grant.Scope.TopologyUID == deleted.TopologyUID {
			return ErrConflict
		}
		if grant.Scope.TopologyUID == current.Scope.TopologyUID && (grant.ActionID != current.ActionID || grant.CommandID != current.CommandID || grant.Attempt != current.Attempt || grant.AgentID != current.AgentID || grant.Scope != current.Scope) {
			return ErrConflict
		}
	}
	return nil
}
