package hostsupervisor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// FinishReturnedDestroyFailure requires synchronous executor return and a full
// journal read under topology exclusion. EffectComplete is written only after
// the deletion callback returns. It does not prove resource absence: canonical
// recovery must independently observe each target before removing state.
func FinishReturnedDestroyFailure(ctx context.Context, children *RunChildren, run *controlplane.Run, actions []runtime.ActionRecord) error {
	if children == nil || run == nil || run.ID == "" || run.Topology == "" || run.ParentID != "" || run.Op != "destroy" || run.Status != controlplane.RunFailed || run.QueuedAt.IsZero() || run.EndedAt.IsZero() {
		return fmt.Errorf("returned failed Destroy required")
	}
	found := false
	for _, action := range actions {
		if action.RunID != run.ID {
			if action.Status != runtime.ActionSucceeded {
				return fmt.Errorf("another Run remains unresolved")
			}
			continue
		}
		if action.Topology != run.Topology || !action.RunQueuedAt.Equal(run.QueuedAt) || action.RunRevision != run.Revision || action.Action != controlplane.PlanActionDelete || !action.EffectComplete || (action.Status != runtime.ActionSucceeded && action.Status != runtime.ActionUncertain) {
			return fmt.Errorf("Destroy effect outcome remains uncertain")
		}
		candidate := action.Candidate
		if action.ID == "" || action.OwnerUID == "" || action.ResourceUID == "" || action.Generation < 1 || candidate == nil || !candidate.Address.Equal(action.Resource) || candidate.OwnerUID != action.OwnerUID || candidate.ResourceUID != action.ResourceUID || candidate.Generation != action.Generation {
			return fmt.Errorf("Destroy target identity unavailable")
		}
		found = true
	}
	if !found {
		return fmt.Errorf("Destroy has no durable deletion evidence")
	}
	if err := children.ProveStopped(ctx); err != nil {
		return err
	}
	failed := *run
	failed.Inputs, failed.Cleanup, failed.HostCompletion = nil, nil, nil
	failed.Recoverable = true
	raw, err := json.Marshal(failed)
	if err != nil {
		return err
	}
	return &quiescentRunFailure{evidence: raw}
}
