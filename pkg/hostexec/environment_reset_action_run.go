package hostexec

import (
	"context"

	"github.com/oslab/sysbox/pkg/persistence"
)

// ActionRun supplies the action owner for a host-authenticated execution Run.
// A ParentID alone is insufficient: signed parent stop proof and committed
// continuation admission must both match its immutable incarnation.
func (b *Environment) ActionRun(ctx context.Context, topology, executionID string) (string, error) {
	run, err := b.Store.GetRun(ctx, executionID)
	if err != nil {
		return "", err
	}
	if run == nil || run.ID != executionID || run.Topology != topology {
		return "", persistence.ErrConflict
	}
	if run.ParentID == "" || run.Op != "reset" {
		return executionID, nil
	}
	parent, err := ResolveResetOrigin(ctx, b, run)
	if err != nil {
		return "", err
	}
	repo, err := b.Repository(topology)
	if err != nil {
		return "", err
	}
	proof, err := repo.LoadResetContinuation(ctx, topology, parent.ID)
	if err != nil {
		return "", err
	}
	if proof.Request.Checkpoint.RunID != parent.ID || proof.Request.Checkpoint.Topology != topology || !proof.Request.RunQueuedAt.Equal(parent.QueuedAt) || proof.Request.RunRevision != parent.Revision {
		return "", persistence.ErrConflict
	}
	return parent.ID, nil
}
