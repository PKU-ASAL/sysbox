package agentexec

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type cancellationTask struct{ cancelled bool }

func (*cancellationTask) Start(context.Context, substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	return substrate.ExecutionHandle{}, nil
}
func (*cancellationTask) ObserveExecution(context.Context, substrate.ExecutionHandle) (substrate.ExecResult, error) {
	return substrate.ExecResult{Status: substrate.ExecStatusRunning}, nil
}
func (c *cancellationTask) CancelExecution(context.Context, substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	c.cancelled = true
	return substrate.RemoteTerminationUnknown, nil
}
func (*cancellationTask) ReadExecutionOutput(context.Context, substrate.ExecutionHandle, string, int64, int) (substrate.ExecutionOutput, error) {
	return substrate.ExecutionOutput{}, nil
}
func (*cancellationTask) RenewExecution(context.Context, substrate.ExecutionHandle) error { return nil }
func (*cancellationTask) GuestTaskBootID(context.Context) (string, error)                 { return "boot", nil }
func TestGrantedTaskCancellationDoesNotInventStoppedEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &cancellationTask{}
	now := time.Now()
	grant := controlplane.SignedExecutionGrant{IssuedAt: now, Grant: controlplane.ExecutionGrant{ExpiresAt: now.Add(time.Minute)}}
	result, err := awaitGrantedTask(ctx, Options{}, client, substrate.ExecutionHandle{}, "", "", grant, func(controlplane.SignedExecutionGrant) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, client.cancelled)
	require.False(t, result.ExitCodeKnown)
	require.Equal(t, substrate.RemoteTerminationUnknown, result.RemoteTermination)
}
