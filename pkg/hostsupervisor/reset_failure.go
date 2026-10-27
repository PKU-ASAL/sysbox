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
// The caller holds execution exclusion while reading actions and proving stop,
// and verifies every predecessor if original is an ancestor continuation Run.
func FinishReturnedResetFailure(ctx context.Context, children *RunChildren, execution, original *controlplane.Run, actions []runtime.ActionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if children == nil {
		return fmt.Errorf("reset child inventory unavailable")
	}
	raw, err := ResetFailureEvidence(execution, original, actions)
	if err != nil {
		return err
	}
	if err := children.ProveStopped(ctx); err != nil {
		return err
	}
	return &quiescentRunFailure{evidence: raw}
}

// ResetFailureEvidence validates durable phases only. A caller must separately
// exclude the old executor, confirm child stops and authenticate continuation
// ancestry before using this evidence as a failed Run completion.
func ResetFailureEvidence(execution, original *controlplane.Run, actions []runtime.ActionRecord) (json.RawMessage, error) {
	if execution == nil || original == nil || execution.Op != "reset" || execution.Status != controlplane.RunFailed || execution.EndedAt.IsZero() || original.Op != "reset" || original.QueuedAt.IsZero() || execution.Topology != original.Topology || execution.Revision != original.Revision || (execution.ID != original.ID && execution.ParentID == "") {
		return nil, fmt.Errorf("returned reset identity required")
	}
	found := false
	for _, action := range actions {
		if action.RunID != original.ID {
			if !runtime.ActionSettled(action) {
				return nil, fmt.Errorf("another Run remains unresolved")
			}
			continue
		}
		if action.Topology != original.Topology || action.Action != controlplane.PlanActionReset || !action.RunQueuedAt.Equal(original.QueuedAt) || action.RunRevision != original.Revision || !action.ResetContinuationSafe() {
			return nil, fmt.Errorf("reset effect outcome remains uncertain")
		}
		found = true
	}
	if !found {
		return nil, fmt.Errorf("reset has no durable action evidence")
	}
	copy := *execution
	copy.Inputs, copy.Cleanup, copy.HostCompletion = nil, nil, nil
	copy.Recoverable = true
	return json.Marshal(copy)
}
