package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// A converging run carrying an explicit per-apply deadline_at must be failed
// once that instant passes, even when the config-default convergence timeout
// (StartedAt + timeout) would still be in the future.
func TestConvergenceDeadlineUsesPerRunDeadline(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunRunning
	run.StartedAt = time.Now().Add(-10 * time.Minute)
	run.DeadlineAt = time.Now().Add(-1 * time.Minute) // already past
	s.jobs.replace(run)

	// The default timeout would NOT have fired yet (10m < 15m).
	s.jobs.markConvergenceDeadlineExceeded(time.Now(), 15*time.Minute)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunFailed, got.Status)
	require.Contains(t, got.Err, "deadline")
}

// Without an explicit deadline_at the sweep keeps falling back to
// StartedAt + timeout: a run that outlived the default window still fails.
func TestConvergenceDeadlineFallsBackToTimeout(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunRunning
	run.StartedAt = time.Now().Add(-30 * time.Minute)
	s.jobs.replace(run)

	s.jobs.markConvergenceDeadlineExceeded(time.Now(), 15*time.Minute)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunFailed, got.Status)
	require.Contains(t, got.Err, "deadline")
}
