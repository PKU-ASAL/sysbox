package substrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type foregroundOnly struct{}

func (foregroundOnly) Exec(context.Context, ExecRequest, io.Writer, io.Writer) (ExecResult, error) {
	return ExecResult{}, nil
}

var _ Connection = foregroundOnly{}

func TestExecCaptureBoundsRetentionAndStreamsWhileDraining(t *testing.T) {
	var stream bytes.Buffer
	capture := NewExecCapture(3, &stream)
	n, err := capture.Write([]byte("abcdef"))
	require.NoError(t, err)
	require.Equal(t, 6, n)
	require.Equal(t, "abc", capture.String())
	require.Equal(t, "abc", stream.String())
	require.True(t, capture.Truncated())
	n, err = capture.Write([]byte("more"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, "abc", capture.String())
}

func TestExecCaptureExactLimitDoesNotTruncate(t *testing.T) {
	capture := NewExecCapture(3, nil)
	_, err := capture.Write([]byte("abc"))
	require.NoError(t, err)
	require.False(t, capture.Truncated())
}

func TestExecutionResultSeparatesRemoteExitFromLocalCancellation(t *testing.T) {
	result, err := FinishExecution(context.Background(), ExecResult{ExitCode: 9, ExitCodeKnown: true}, nil)
	require.NoError(t, err)
	require.Equal(t, ExecStatusExited, result.Status)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = FinishExecution(ctx, ExecResult{Stdout: "partial"}, errors.New("connection closed"))
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, ExecStatusUnknown, result.Status)
	require.Equal(t, ExecErrorCanceled, result.ErrorClass)
	require.Equal(t, RemoteTerminationUnknown, result.RemoteTermination)
	require.False(t, result.ExitCodeKnown)
	require.Equal(t, "partial", result.Stdout)
}

func TestExecutionBudgetHonorsEarlierParentDeadline(t *testing.T) {
	parent, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	ctx, cancel := (ExecRequest{Timeout: time.Hour}).Context(parent)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	deadline, _ := ctx.Deadline()
	require.Equal(t, parentDeadline, deadline)
}

func TestExecutionRequestRejectsNegativeBudgets(t *testing.T) {
	require.Error(t, (ExecRequest{Program: "true", Shell: ShellNone, OutputLimitBytes: -1}).Validate())
	require.Error(t, (ExecRequest{Program: "true", Shell: ShellNone, Timeout: -time.Second}).Validate())
}
