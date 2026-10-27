package controlplane

import (
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestGuestTaskResultRequiresTerminationAndPreservesUnknown(t *testing.T) {
	_, err := NewGuestTaskResult(substrate.ExecResult{ExitCodeKnown: true, Status: substrate.ExecStatusExited})
	require.Error(t, err)
	unknown, err := NewGuestTaskResult(substrate.ExecResult{RemoteTermination: substrate.RemoteTerminationConfirmed})
	require.NoError(t, err)
	require.Equal(t, "unknown", unknown.ResultClass)
	require.False(t, unknown.ExitCodeKnown)
	require.NoError(t, unknown.Validate())
	exit, err := NewGuestTaskResult(substrate.ExecResult{RemoteTermination: substrate.RemoteTerminationConfirmed, ExitCodeKnown: true, ExitCode: 7, Stdout: strings.Repeat("x", substrate.DefaultExecOutputLimit+1)})
	require.NoError(t, err)
	require.Equal(t, 7, exit.Result.ExitCode)
	require.True(t, exit.Result.Truncated)
	require.NoError(t, exit.Validate())
	exit.ExitCodeKnown = false
	require.Error(t, exit.Validate())
	unknown.ResultClass = "invented"
	require.Error(t, unknown.Validate())
}
