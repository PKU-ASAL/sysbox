package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestTaskResultIsDurableBoundedAndRetriedAfterRestart(t *testing.T) {
	d, req, conn := taskFixture(t)
	ctx := context.Background()
	started, err := d.Start(ctx, testRef(req), req)
	require.NoError(t, err)
	conn.result = substrate.ExecResult{Status: substrate.ExecStatusExited, ExitCode: 7, ExitCodeKnown: true, RemoteTermination: substrate.RemoteTerminationConfirmed, Stdout: strings.Repeat("x", substrate.DefaultExecOutputLimit+1), Stderr: "error"}
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { return errors.New("offline") }
	stopped, err := d.Observe(ctx, testRef(req), started.Handle)
	require.ErrorContains(t, err, "offline")
	require.NotNil(t, stopped.StopReceipt)
	require.False(t, stopped.Reported)
	outcome := stopped.StopReceipt.GuestResult
	require.True(t, outcome.Result.Truncated)
	require.True(t, stopped.Result.StdoutTruncated)
	require.Equal(t, 7, outcome.Result.ExitCode)
	data, err := base64.StdEncoding.DecodeString(outcome.Result.Stdout)
	require.NoError(t, err)
	require.Len(t, data, substrate.DefaultExecOutputLimit)
	require.NoError(t, controlplane.VerifyExecutionStopReceipt(d.HostKey.Public().(ed25519.PublicKey), *stopped.StopReceipt))
	cold := *d
	calls := 0
	cold.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		calls++
		require.Equal(t, *stopped.StopReceipt, r)
		return nil
	}
	cold.Resolve = nil // sealed results need neither a worker nor a live provider.
	require.NoError(t, cold.Recover(ctx))
	require.Equal(t, 1, calls)
	recovered, err := cold.load(started.Handle)
	require.NoError(t, err)
	require.True(t, recovered.Reported)
}

func TestTaskExitWithoutQuiescenceCannotSealResult(t *testing.T) {
	d, req, conn := taskFixture(t)
	ctx := context.Background()
	started, err := d.Start(ctx, testRef(req), req)
	require.NoError(t, err)
	conn.result = substrate.ExecResult{Status: substrate.ExecStatusExited, ExitCodeKnown: true, RemoteTermination: substrate.RemoteTerminationUnknown}
	observed, err := d.Observe(ctx, testRef(req), started.Handle)
	require.NoError(t, err)
	require.Nil(t, observed.StopReceipt)
}

func TestTaskResultPreservesProviderTruncation(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			d, req, conn := taskFixture(t)
			ctx := context.Background()
			started, err := d.Start(ctx, testRef(req), req)
			require.NoError(t, err)
			conn.result = substrate.ExecResult{Status: substrate.ExecStatusExited, ExitCodeKnown: true, RemoteTermination: substrate.RemoteTerminationConfirmed, Stdout: "short", Stderr: "short"}
			conn.result.StdoutTruncated = stream == "stdout"
			conn.result.StderrTruncated = stream == "stderr"
			stopped, err := d.Observe(ctx, testRef(req), started.Handle)
			require.NoError(t, err)
			require.NotNil(t, stopped.StopReceipt)
			require.True(t, stopped.StopReceipt.GuestResult.Result.Truncated)
		})
	}
}
