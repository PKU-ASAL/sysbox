package hostsupervisor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// FinishReturnedResetFailure is for the trusted synchronous executor only.
// A cold journal or a timed-out callback cannot call this to infer termination.
// The caller holds execution exclusion while reading actions and proving stop.
func FinishReturnedResetFailure(ctx context.Context, children *RunChildren, execution, original *controlplane.Run, actions []runtime.ActionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if children == nil || execution == nil || original == nil || execution.Op != "reset" || execution.Status != controlplane.RunFailed || execution.EndedAt.IsZero() || original.Op != "reset" || original.QueuedAt.IsZero() || execution.Topology != original.Topology || (execution.ID != original.ID && execution.ParentID != original.ID) {
		return fmt.Errorf("returned reset identity required")
	}
	found := false
	for _, action := range actions {
		if action.RunID != original.ID {
			if action.Status != runtime.ActionSucceeded {
				return fmt.Errorf("another Run remains unresolved")
			}
			continue
		}
		if action.Topology != original.Topology || action.Action != controlplane.PlanActionReset || !action.RunQueuedAt.Equal(original.QueuedAt) || action.RunRevision != original.Revision || !action.ResetContinuationSafe() {
			return fmt.Errorf("reset effect outcome remains uncertain")
		}
		found = true
	}
	if !found {
		return fmt.Errorf("reset has no durable action evidence")
	}
	if err := children.ProveStopped(ctx); err != nil {
		return err
	}
	copy := *execution
	copy.Inputs, copy.Cleanup, copy.HostCompletion = nil, nil, nil
	copy.Recoverable = true
	raw, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	return &quiescentRunFailure{evidence: raw}
}
