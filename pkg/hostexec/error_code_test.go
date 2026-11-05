package hostexec

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestRunErrorCodeUsesTypedCauseNotText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{nil, ""}, {fmt.Errorf("wrapped: %w", runtime.ErrObservationUnknown), "observation_unknown"},
		{runtime.ErrActionUncertain, "action_uncertain"}, {runtime.ErrPlanStale, "plan_stale"},
		{persistence.ErrNotFound, "state_missing"}, {persistence.ErrConflict, "state_conflict"},
		{context.DeadlineExceeded, "deadline_exceeded"}, {context.Canceled, "cancelled"},
		{runtime.ErrConvergenceUnsupported, "unsupported"}, {errors.New("observation unknown"), "operation_failed"},
	} {
		require.Equal(t, tc.code, runErrorCode(tc.err))
	}
}
