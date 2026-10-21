package persistence

import (
	"context"
	"math"
)

// Retired sources remain immutable reference holders. Retirement fences future
// publishers; it does not attest provider stop or authorize reference removal.
const GCSourceRetirementFormatVersion = 8

func (r *Repository) RetireGCSource(ctx context.Context, topology, source, owner string, expected int64) error {
	if !topologyName.MatchString(source) || expected < 1 || expected == math.MaxInt64 {
		return ErrInvalidRecord
	}
	return r.update(ctx, topology, func(a *aggregate) error {
		if a.State.TopologyUID != owner {
			return ErrConflict
		}
		s, ok := a.GCSources[source]
		if !ok {
			return ErrNotFound
		}
		if s.Retired && s.Version == expected+1 {
			return nil
		}
		if s.Retired || s.Updating || s.Version != expected {
			return ErrConflict
		}
		s.Retired = true
		s.Version++
		a.GCSources[source] = s
		if a.Format != GCDispositionFormatVersion && a.Format != OwnershipRetirementFormatVersion && a.Format != OwnershipTargetFormatVersion {
			a.Format = GCSourceRetirementFormatVersion
		}
		return nil
	})
}
