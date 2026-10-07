package runtime

import (
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// CheckpointResumeSafety is conservative: a durable started marker is evidence
// of possible execution, not evidence that a guest command may be repeated.
// Completed state patches can be replayed; incomplete effects require operator
// inspection or a provider-specific idempotent recovery protocol.
func CheckpointResumeSafety(cp OperationCheckpoint) error {
	for _, step := range cp.Steps {
		if step.Kind == "state" {
			continue
		}
		if step.Kind != "resource" && step.Kind != "subaction" {
			continue
		}
		if step.Status != OperationDone {
			return fmt.Errorf("resume blocked: unconfirmed effect %s/%s", step.Resource, step.Phase)
		}
		if step.Kind == "resource" && !step.StateRecorded && step.StateResource == nil && step.Action != controlplane.PlanActionDelete && step.Action != controlplane.PlanActionRead {
			return fmt.Errorf("resume blocked: completed effect %s lacks recoverable state receipt", step.Resource)
		}
	}
	return nil
}
