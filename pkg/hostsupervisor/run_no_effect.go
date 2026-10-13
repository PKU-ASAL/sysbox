package hostsupervisor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// FinishReturnedRunWithoutActions is only for the trusted synchronous executor
// after return, while holding topology exclusion and reading the full journal.
// It must never be used to infer quiescence from a cold intent or an empty state.
func FinishReturnedRunWithoutActions(ctx context.Context, children *RunChildren, run *controlplane.Run, actions []runtime.ActionRecord) error {
	if children == nil || run == nil || run.ID == "" || run.Topology == "" || run.ParentID != "" || run.Status != controlplane.RunFailed || run.QueuedAt.IsZero() || run.EndedAt.IsZero() || !controlplane.IsRunExecutionOperation("run_"+run.Op) {
		return fmt.Errorf("returned failed lifecycle execution required")
	}
	for _, action := range actions {
		if action.RunID == run.ID || action.Status != runtime.ActionSucceeded {
			return fmt.Errorf("resource action evidence prevents no-effect completion")
		}
	}
	if err := children.ProveStopped(ctx); err != nil {
		return err
	}
	failed := *run
	failed.Inputs, failed.Cleanup, failed.HostCompletion = nil, nil, nil
	failed.Recoverable = false
	raw, err := json.Marshal(failed)
	if err != nil {
		return err
	}
	return &quiescentRunFailure{evidence: raw}
}
