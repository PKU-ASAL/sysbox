package persistence

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// MaintenanceBarrier is durable exclusion of new provider authority. It is not
// evidence of quiescence or a completed restore. RequestDigest binds the exact
// immutable maintenance plan, while ID is the caller's stable idempotency key.
type MaintenanceBarrier struct {
	ID            string    `json:"id"`
	TopologyUID   string    `json:"topology_uid"`
	RequestDigest string    `json:"request_digest"`
	Epoch         uint64    `json:"epoch"`
	CreatedAt     time.Time `json:"created_at"`
	Released      bool      `json:"released"`
}

func (r *ExecutionGrantRepository) InspectMaintenance(ctx context.Context, id string) (*MaintenanceBarrier, error) {
	var result MaintenanceBarrier
	err := r.read(ctx, func(a *executionAuthority) error {
		var exists bool
		result, exists = a.Maintenance[id]
		if !exists {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// ValidateMaintenanceWrite checks an authority snapshot while its storage lock
// is held through metadata commit. Nil means no authority has yet been issued.
// This does not permit provider effects or substitute for command validation.
func ValidateMaintenanceWrite(raw []byte, topologyUID string, agents ...string) error {
	if topologyUID == "" {
		return ErrInvalidRecord
	}
	authority, err := decodeExecutionAuthority(raw)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if authority.hostFenceBlocks(agent) {
			return ErrConflict
		}
	}
	if authority.maintenanceBlocks(topologyUID) {
		return ErrConflict
	}
	return nil
}

func (a *executionAuthority) maintenanceBlocks(topology string) bool {
	for _, barrier := range a.Maintenance {
		if barrier.TopologyUID == topology && !barrier.Released {
			return true
		}
	}
	return false
}

func validateMaintenance(a *executionAuthority) error {
	active := make(map[string]bool)
	for id, barrier := range a.Maintenance {
		if id == "" || id != barrier.ID || barrier.TopologyUID == "" || strings.TrimSpace(barrier.RequestDigest) == "" || barrier.Epoch == 0 || barrier.Epoch > a.Epoch || barrier.CreatedAt.IsZero() {
			return ErrCorruptRecord
		}
		if !barrier.Released {
			if active[barrier.TopologyUID] {
				return ErrCorruptRecord
			}
			active[barrier.TopologyUID] = true
		}
	}
	for _, grant := range a.Grants {
		if active[grant.Scope.TopologyUID] && grant.State == controlplane.GrantActive {
			return ErrCorruptRecord
		}
	}
	return nil
}

// BeginMaintenance freezes one topology and revokes all of its live grants in
// the same authority transaction. It never fabricates stop receipts. Its record
// survives crashes and has no timeout that could silently reopen execution.
func (r *ExecutionGrantRepository) BeginMaintenance(ctx context.Context, id, topologyUID, digest string) (*MaintenanceBarrier, error) {
	if !topologyName.MatchString(id) || topologyUID == "" || strings.TrimSpace(digest) == "" {
		return nil, ErrInvalidRecord
	}
	var result MaintenanceBarrier
	err := r.update(ctx, func(a *executionAuthority) error {
		if prior, exists := a.Maintenance[id]; exists {
			if prior.TopologyUID != topologyUID || prior.RequestDigest != digest {
				return ErrConflict
			}
			result = prior
			return nil
		}
		if a.maintenanceBlocks(topologyUID) || a.Epoch == math.MaxUint64 {
			return ErrConflict
		}
		a.Epoch++
		result = MaintenanceBarrier{ID: id, TopologyUID: topologyUID, RequestDigest: digest, Epoch: a.Epoch, CreatedAt: a.ObservedAt}
		if a.Maintenance == nil {
			a.Maintenance = make(map[string]MaintenanceBarrier)
		}
		a.Maintenance[id] = result
		for key, grant := range a.Grants {
			if grant.Scope.TopologyUID == topologyUID && grant.State == controlplane.GrantActive {
				grant.State = controlplane.GrantRevoked
				a.Grants[key] = grant
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// EndMaintenance releases only the exact barrier after all original scopes
// have confirmed stops. Callers must first finish or explicitly abandon their
// maintenance transaction. An old released token cannot clear a newer barrier.
func (r *ExecutionGrantRepository) EndMaintenance(ctx context.Context, proof MaintenanceBarrier) error {
	return r.update(ctx, func(a *executionAuthority) error {
		current, exists := a.Maintenance[proof.ID]
		if !exists {
			return ErrNotFound
		}
		claimed := proof
		claimed.Released = current.Released
		if claimed != current {
			return ErrConflict
		}
		if current.Released {
			return nil
		}
		for _, record := range a.Fences {
			if record.BarrierID == current.ID {
				return ErrConflict
			}
		}
		for _, grant := range a.Grants {
			if grant.Scope.TopologyUID == current.TopologyUID && grant.State != controlplane.GrantStopped {
				return ErrConflict
			}
		}
		current.Released = true
		a.Maintenance[current.ID] = current
		return nil
	})
}
