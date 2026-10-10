package persistence

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// CommitResetIntents publishes one Run's reset intents in a single aggregate
// transaction. Validation or backend failure must never expose a partial batch.
func (r *Repository) CommitResetIntents(ctx context.Context, batch []runtime.ActionRecord) error {
	if len(batch) == 0 {
		return ErrInvalidRecord
	}
	first := batch[0]
	ids, resources := map[string]bool{}, map[string]bool{}
	for _, a := range batch {
		if err := validateAction(a); err != nil {
			return err
		}
		if a.Action != controlplane.PlanActionReset || a.Phase != "accepted" || a.Status != runtime.ActionIntentCommitted || a.Revision != 0 || a.EffectComplete || a.Topology != first.Topology || a.RunID != first.RunID || a.OwnerUID != first.OwnerUID || !a.RunQueuedAt.Equal(first.RunQueuedAt) || a.RunRevision != first.RunRevision || a.StateSerial != first.StateSerial || a.ExpectedSerial != first.ExpectedSerial || ids[a.ID] || resources[a.Resource.String()] {
			return ErrConflict
		}
		ids[a.ID], resources[a.Resource.String()] = true, true
	}
	return r.update(ctx, first.Topology, func(a *aggregate) error {
		for _, action := range batch {
			if err := commitActionIntent(a, action); err != nil {
				return err
			}
		}
		return nil
	})
}
