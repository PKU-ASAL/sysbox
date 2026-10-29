package hostexec

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// ResolveResetOrigin verifies each stopped predecessor, retaining the actual
// ParentID chain for audit while returning the Run that owns durable actions.
// A receipt for the root does not establish that intermediate executors stopped.
func ResolveResetOrigin(ctx context.Context, bridge Bridge, run *controlplane.Run) (*controlplane.Run, error) {
	if run == nil || run.ID == "" || run.Op != "reset" {
		return nil, fmt.Errorf("reset execution identity required")
	}
	verifier, ok := bridge.(ResetContinuationVerifier)
	if !ok {
		return nil, fmt.Errorf("reset continuation requires verified prior executor return evidence")
	}
	seen := map[string]bool{run.ID: true}
	current := run
	for current.ParentID != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[current.ParentID] || len(seen) >= 1024 {
			return nil, fmt.Errorf("reset ancestry cycle or excessive depth")
		}
		parent, err := bridge.ParentRun(ctx, current.ParentID)
		if err != nil {
			return nil, err
		}
		if parent == nil || parent.ID != current.ParentID || parent.Topology != run.Topology || parent.Op != "reset" || parent.Revision != run.Revision || parent.QueuedAt.IsZero() || !parent.Status.IsTerminal() {
			return nil, fmt.Errorf("reset predecessor identity changed or execution is not terminal")
		}
		if err := verifier.VerifyResetContinuation(ctx, current, parent); err != nil {
			return nil, err
		}
		seen[parent.ID] = true
		current = parent
	}
	return current, nil
}
