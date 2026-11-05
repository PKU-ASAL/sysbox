package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestDeadlineFailureAcceptsHostStopWithoutChangingBusinessResult(t *testing.T) {
	now := time.Now().UTC()
	current := controlplane.Run{ID: "run", Topology: "lab", AgentID: "host", Op: "apply", Status: controlplane.RunFailed, ErrorCode: "convergence_deadline_exceeded", Err: "convergence deadline exceeded", Recoverable: true, QueuedAt: now.Add(-time.Minute), StartedAt: now.Add(-time.Minute), EndedAt: now.Add(-time.Second), Attempt: 1, LeaseOwner: "worker", LeaseUntil: now.Add(time.Minute)}
	for _, status := range []controlplane.RunStatus{controlplane.RunDone, controlplane.RunFailed, controlplane.RunCancelled} {
		t.Run(string(status), func(t *testing.T) {
			observed := current
			observed.Status = status
			observed.EndedAt = now
			observed.Err = ""
			observed.ErrorCode = ""
			if status != controlplane.RunDone {
				observed.Err = "host cancelled"
				observed.ErrorCode = "cancelled"
			}
			next, err := CompleteRunFromHost(current, observed, now)
			require.NoError(t, err)
			require.Equal(t, controlplane.RunFailed, next.Status)
			require.Equal(t, current.ErrorCode, next.ErrorCode)
			require.Equal(t, current.Err, next.Err)
			require.Equal(t, current.EndedAt, next.EndedAt)
			require.True(t, next.LeaseUntil.IsZero())
			require.Nil(t, next.Cleanup)
		})
	}
	unrelated := current
	unrelated.ErrorCode = "deadline_exceeded"
	observed := current
	observed.EndedAt = now
	_, err := CompleteRunFromHost(unrelated, observed, now)
	require.ErrorIs(t, err, ErrConflict)
}
