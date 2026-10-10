package sshaccess

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func runnerScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sshd")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700))
	return path
}

func TestSSHDRunnerSeparatesDiagnosticsAndReturnsOnlySuccessfulOutput(t *testing.T) {
	for _, tc := range []struct {
		body string
		fail bool
	}{
		{"printf 'authorizedkeyscommand none\\n'; printf 'diagnostic\\n' >&2", false},
		{"printf 'incomplete\\n'; exit 1", true},
	} {
		run, err := NewSSHDRunner(runnerScript(t, tc.body))
		require.NoError(t, err)
		out, err := run(context.Background(), "-T", "-f", "/tmp/config", "-C", "user=test")
		if tc.fail {
			require.Error(t, err)
			require.Empty(t, out)
		} else {
			require.NoError(t, err)
			require.Equal(t, "authorizedkeyscommand none\n", string(out))
		}
	}
}

func TestSSHDRunnerCancelsOnOutputOverflow(t *testing.T) {
	for _, redirect := range []string{"", " >&2"} {
		run, err := NewSSHDRunner(runnerScript(t, "while :; do printf '0123456789012345678901234567890123456789012345678901234567890123456789\\n'"+redirect+"; done"))
		require.NoError(t, err)
		start := time.Now()
		out, err := run(context.Background(), "-t", "-f", "/tmp/config")
		require.ErrorContains(t, err, "output limit")
		require.Empty(t, out)
		require.Less(t, time.Since(start), 5*time.Second)
	}
}

func TestSSHDRunnerHonorsCancellationAndRejectsDaemonArguments(t *testing.T) {
	run, err := NewSSHDRunner(runnerScript(t, "exec sleep 30"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = run(ctx, "-t", "-f", "/tmp/config")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
	for _, args := range [][]string{{"-D"}, {"-t", "-f", "relative"}, {"-T", "-f", "/tmp/config", "-o", "Port=2222"}} {
		_, err = run(context.Background(), args...)
		require.ErrorContains(t, err, "validation arguments")
	}
	_, err = NewSSHDRunner("sshd")
	require.Error(t, err)
}

func TestSSHDRunnerBoundsInvocationWithoutCallerDeadline(t *testing.T) {
	run, err := NewSSHDRunner(runnerScript(t, "exec sleep 30"))
	require.NoError(t, err)
	start := time.Now()
	out, err := run(context.Background(), "-t", "-f", "/tmp/config")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, out)
	require.Less(t, time.Since(start), sshdProbeTimeout+2*time.Second)
}
