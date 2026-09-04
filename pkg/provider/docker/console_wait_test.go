package docker

import (
	"errors"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

// A console session whose exec never exits must not leave a goroutine polling
// the daemon for the lifetime of the process.
//
// The loop used to run on context.Background() with no stop condition, so once
// the websocket relay returned, the wait goroutine kept inspecting every 100ms
// forever — one leaked pair per timed-out console, unreclaimable because the
// context it held could never be cancelled.
func TestWaitForExecExitStopsWhenSessionCloses(t *testing.T) {
	done := make(chan struct{})
	close(done)
	alwaysRunning := func() (container.ExecInspect, error) {
		return container.ExecInspect{Running: true}, nil
	}

	_, err := waitForExecExit(done, time.Millisecond, alwaysRunning)

	require.Error(t, err, "a closed session must end the wait rather than poll forever")
}

// The normal path still reports the exec's exit code.
func TestWaitForExecExitReportsExitCode(t *testing.T) {
	done := make(chan struct{})
	finished := func() (container.ExecInspect, error) {
		return container.ExecInspect{Running: false, ExitCode: 3}, nil
	}

	code, err := waitForExecExit(done, time.Millisecond, finished)

	require.NoError(t, err)
	require.Equal(t, 3, code)
}

// An inspect failure is surfaced, not retried forever.
func TestWaitForExecExitSurfacesInspectFailure(t *testing.T) {
	done := make(chan struct{})
	inspectErr := errors.New("daemon unreachable")
	failing := func() (container.ExecInspect, error) {
		return container.ExecInspect{}, inspectErr
	}

	_, err := waitForExecExit(done, time.Millisecond, failing)

	require.ErrorIs(t, err, inspectErr)
}

// A still-running exec that then finishes is reported once it does: the stop
// channel bounds the wait, it does not cut a healthy one short.
func TestWaitForExecExitWaitsForAStillRunningExec(t *testing.T) {
	done := make(chan struct{})
	calls := 0
	settling := func() (container.ExecInspect, error) {
		calls++
		if calls < 3 {
			return container.ExecInspect{Running: true}, nil
		}
		return container.ExecInspect{Running: false, ExitCode: 0}, nil
	}

	code, err := waitForExecExit(done, time.Millisecond, settling)

	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.GreaterOrEqual(t, calls, 3)
}
