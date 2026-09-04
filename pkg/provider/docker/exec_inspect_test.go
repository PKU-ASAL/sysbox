package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

// A hijacked stream can end while the exec'd process is still alive: the daemon
// restarts, a proxy resets an idle connection, or a mid-stream close lands on a
// frame boundary so StdCopy reports a clean EOF. ContainerExecInspect then
// reports Running=true with ExitCode left at its zero value.
//
// Reporting that zero as the command's exit code is the worst available
// failure: a `check` asserting exit_code == 0 passes on a command that never
// finished, and it passes with a truncated body. Nothing downstream can tell
// this apart from real success. backgroundExecStatus already refuses to trust
// a still-running inspect; the foreground path must refuse too.
func TestForegroundExecStatusRejectsStillRunningInspect(t *testing.T) {
	_, err := foregroundExecStatus(container.ExecInspect{Running: true, ExitCode: 0})

	require.Error(t, err, "a still-running exec must not yield an exit code")
	require.Contains(t, err.Error(), "still running")
}

// A finished exec is the only case that may report an exit code.
func TestForegroundExecStatusReportsExitCodeWhenFinished(t *testing.T) {
	code, err := foregroundExecStatus(container.ExecInspect{Running: false, ExitCode: 0})

	require.NoError(t, err)
	require.Equal(t, 0, code)
}

// A non-zero exit is the command's own verdict, not a transport failure: it
// must come back as a value, not an error, or every failing guest command would
// be misreported as a provider fault.
func TestForegroundExecStatusTreatsNonZeroExitAsValue(t *testing.T) {
	code, err := foregroundExecStatus(container.ExecInspect{Running: false, ExitCode: 7})

	require.NoError(t, err)
	require.Equal(t, 7, code)
}
