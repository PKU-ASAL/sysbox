package hostsupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// quiescentRunFailure can only be constructed after successful synchronous
// resource execution and exhaustive child-stop evidence. Arbitrary callback
// errors, lost daemon responses and cold intents must never use this path.
type quiescentRunFailure struct{ evidence json.RawMessage }

func (e *quiescentRunFailure) Error() string { return "run failed after confirmed child termination" }

func FinishRunChildren(ctx context.Context, children *RunChildren, run *controlplane.Run) error {
	if children == nil || run == nil || run.Status != controlplane.RunDone {
		return fmt.Errorf("successful resource execution required")
	}
	if err := children.Finish(ctx, run); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = children.Cancel(cleanup)
		if proofErr := children.ProveStopped(cleanup); proofErr != nil {
			return fmt.Errorf("child stop remains uncertain: %w", proofErr)
		}
		failed := *run
		failed.Status = controlplane.RunFailed
		failed.Err = "run child execution failed after confirmed termination"
		failed.EndedAt = time.Now().UTC()
		failed.Inputs = nil
		failed.Cleanup = nil
		raw, marshalErr := json.Marshal(failed)
		if marshalErr != nil {
			return marshalErr
		}
		return &quiescentRunFailure{evidence: raw}
	}
	return nil
}
