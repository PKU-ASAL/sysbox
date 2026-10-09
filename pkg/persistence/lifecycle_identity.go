package persistence

import "context"

// ValidateLifecycleOwner checks an already-locked aggregate. Callers issuing
// authority must retain their authority lock through commit; a preflight state
// read alone cannot prevent deletion from retiring this UID between the steps.
func ValidateLifecycleOwner(raw []byte, topology, uid string) error {
	if !topologyName.MatchString(topology) || uid == "" {
		return ErrInvalidRecord
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return err
	}
	if err = validateAggregate(a, topology); err != nil {
		return err
	}
	if a.State.TopologyUID != uid {
		return ErrConflict
	}
	return nil
}

// ValidateLifecycleOwner is the file-backed caller's aggregate lock boundary.
func (r *Repository) ValidateLifecycleOwner(ctx context.Context, topology, uid string) error {
	if uid == "" {
		return ErrInvalidRecord
	}
	return r.update(ctx, topology, func(a *aggregate) error {
		if a.State.TopologyUID != uid {
			return ErrConflict
		}
		return nil
	})
}
