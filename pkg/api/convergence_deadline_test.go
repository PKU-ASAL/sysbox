package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func startConvergingRun(t *testing.T, s *Server, op string, startedAgo time.Duration) *controlplane.Run {
	t.Helper()
	run := s.jobs.start("lab", op)
	run.Status = controlplane.RunRunning
	run.StartedAt = time.Now().Add(-startedAgo)
	s.jobs.replace(run)
	return run
}

// A topology that has been converging past its deadline must be declared failed,
// so the convergence bound is not left to every consumer to implement.
func TestMarkConvergenceDeadlineExceededFailsOverdueRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := startConvergingRun(t, s, "apply", 30*time.Minute)

	s.jobs.markConvergenceDeadlineExceeded(time.Now(), 15*time.Minute)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunFailed, got.Status)
	require.Contains(t, got.Err, "convergence deadline exceeded")
	require.True(t, got.Recoverable, "a deadline-exceeded run left partial work and must be recoverable")
}

// A run still within its convergence window must be left alone.
func TestMarkConvergenceDeadlineExceededLeavesFreshRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := startConvergingRun(t, s, "apply", 5*time.Minute)

	s.jobs.markConvergenceDeadlineExceeded(time.Now(), 15*time.Minute)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunRunning, got.Status)
}

// Destroy is not convergence: a long-running destroy must not be failed by the
// convergence deadline.
func TestMarkConvergenceDeadlineExceededIgnoresDestroy(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := startConvergingRun(t, s, "destroy", 30*time.Minute)

	s.jobs.markConvergenceDeadlineExceeded(time.Now(), 15*time.Minute)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunRunning, got.Status)
}

// A disabled deadline (timeout <= 0) must not fail anything: operators who opt
// out of the bound keep unbounded convergence.
func TestMarkConvergenceDeadlineExceededDisabled(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := startConvergingRun(t, s, "apply", 30*time.Minute)

	s.jobs.markConvergenceDeadlineExceeded(time.Now(), 0)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunRunning, got.Status)
}

// A run that is queued but never started still counts: it has been stuck since
// its queue time, which is what StartedAt holds until the run actually begins.
func TestMarkConvergenceDeadlineExceededFailsStuckQueuedRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunQueued
	run.StartedAt = time.Now().Add(-30 * time.Minute)
	s.jobs.replace(run)

	s.jobs.markConvergenceDeadlineExceeded(time.Now(), 15*time.Minute)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunFailed, got.Status)
}
