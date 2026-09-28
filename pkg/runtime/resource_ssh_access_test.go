package runtime

import (
	"context"
	"io"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type sshSetupConnection struct {
	result substrate.ExecResult
	calls  int
}

func (c *sshSetupConnection) Exec(context.Context, substrate.ExecRequest, io.Writer, io.Writer) (substrate.ExecResult, error) {
	c.calls++
	return c.result, nil
}

func TestExecLinuxCommandsRejectsUnknownExitBeforeContinuing(t *testing.T) {
	conn := &sshSetupConnection{result: substrate.ExecResult{ExitCode: 0, ExitCodeKnown: false}}
	err := execLinuxCommands(context.Background(), conn, []string{"first effect", "second effect"})
	require.ErrorIs(t, err, ErrActionUncertain)
	require.ErrorContains(t, err, "exit status unknown")
	require.Equal(t, 1, conn.calls, "unknown completion must not authorize the next command")
}

func TestExecLinuxCommandsAcceptsKnownSuccessfulExit(t *testing.T) {
	conn := &sshSetupConnection{result: substrate.ExecResult{ExitCodeKnown: true}}
	require.NoError(t, execLinuxCommands(context.Background(), conn, []string{"first effect", "second effect"}))
	require.Equal(t, 2, conn.calls)
}
