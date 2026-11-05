package hostexec

import (
	"context"
	"errors"

	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
)

// Classification is diagnostic only. It never establishes absence, cessation
// or permission to discard ownership. Unstructured provider errors stay generic.
func runErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, runtime.ErrActionUncertain):
		return "action_uncertain"
	case errors.Is(err, runtime.ErrObservationUnknown):
		return "observation_unknown"
	case errors.Is(err, runtime.ErrPlanStale):
		return "plan_stale"
	case errors.Is(err, persistence.ErrNotFound):
		return "state_missing"
	case errors.Is(err, persistence.ErrConflict):
		return "state_conflict"
	case errors.Is(err, runtime.ErrConvergenceUnsupported), errors.Is(err, persistence.ErrUnsupportedVersion):
		return "unsupported"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "operation_failed"
	}
}
