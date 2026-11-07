package libvirt

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type windowsObservationConnection struct {
	calls     int
	responses []string
	err       error
	requests  []substrate.ExecRequest
}

func (c *windowsObservationConnection) Exec(ctx context.Context, request substrate.ExecRequest, stdout, stderr io.Writer) (substrate.ExecResult, error) {
	c.calls++
	c.requests = append(c.requests, request)
	if c.err != nil {
		return substrate.ExecResult{}, c.err
	}
	i := c.calls - 1
	if i >= len(c.responses) {
		i = len(c.responses) - 1
	}
	if i >= 0 && stdout != nil {
		_, _ = io.WriteString(stdout, c.responses[i])
	}
	return substrate.ExecResult{ExitCode: 0, ExitCodeKnown: true}, nil
}
func (*windowsObservationConnection) CopyFile(context.Context, string, string) error { return nil }
func (*windowsObservationConnection) Close() error                                   { return nil }

func TestWindowsNetworkObservationWaitsWithoutConfiguring(t *testing.T) {
	c := &windowsObservationConnection{responses: []string{"", "10.40.0.10/24\r\n"}}
	bridges := []BridgeAttach{{Name: "lan", MAC: "02:00:00:00:00:10", IPPrefixes: []string{"10.40.0.10/24"}}}
	observation, err := observeWindowsNetwork(context.Background(), c, bridges, time.Second, time.Millisecond)
	require.NoError(t, err)
	require.True(t, observation.Converged)
	require.Equal(t, 2, c.calls)
	require.True(t, observation.Interfaces[0].Converged)
	for _, request := range c.requests {
		require.Contains(t, request.Program, "Get-NetAdapter")
		require.Contains(t, request.Program, "Get-NetIPAddress")
		require.Contains(t, request.Program, "020000000010")
		for _, effect := range []string{"New-", "Set-", "Remove-", "Restart-"} {
			require.NotContains(t, request.Program, effect)
		}
	}
}
func TestWindowsNetworkObservationFailsClosedAndBoundsWait(t *testing.T) {
	bridges := []BridgeAttach{{Name: "lan", MAC: "02:00:00:00:00:10", IPPrefixes: []string{"10.40.0.10/24"}}}
	c := &windowsObservationConnection{err: errors.New("QGA unavailable")}
	observation, err := observeWindowsNetwork(context.Background(), c, bridges, time.Second, time.Millisecond)
	require.ErrorContains(t, err, "QGA unavailable")
	require.False(t, observation.Converged)
	require.Equal(t, 1, c.calls)
	c = &windowsObservationConnection{responses: []string{"10.40.0.11/24"}}
	observation, err = observeWindowsNetwork(context.Background(), c, bridges, 3*time.Millisecond, time.Millisecond)
	require.NoError(t, err)
	require.False(t, observation.Converged)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = observeWindowsNetwork(ctx, c, bridges, time.Second, time.Millisecond)
	require.ErrorIs(t, err, context.Canceled)
}
func TestWindowsNetworkObservationValidatesAllAttachmentsBeforeIO(t *testing.T) {
	c := &windowsObservationConnection{}
	for _, bridge := range []BridgeAttach{
		{Name: "bad", MAC: "not-a-mac", IPPrefixes: []string{"10.0.0.1/24"}},
		{Name: "bad", MAC: "02:00:00:00:00:10", IPPrefixes: []string{"::1/64"}},
	} {
		_, err := observeWindowsNetwork(context.Background(), c, []BridgeAttach{bridge}, time.Second, time.Millisecond)
		require.Error(t, err)
	}
	require.Zero(t, c.calls)
}
func TestWindowsRouteVerificationOnlyRetriesObservation(t *testing.T) {
	calls := 0
	err := waitWindowsRoute(context.Background(), time.Second, time.Millisecond, func(context.Context) error {
		calls++
		if calls < 3 {
			return errWindowsRouteMissing
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	calls = 0
	err = waitWindowsRoute(context.Background(), time.Second, time.Millisecond, func(context.Context) error { calls++; return errors.New("unknown route state") })
	require.ErrorContains(t, err, "unknown route state")
	require.Equal(t, 1, calls)
	err = waitWindowsRoute(context.Background(), time.Millisecond, time.Millisecond, func(context.Context) error { return errWindowsRouteMissing })
	require.ErrorIs(t, err, errWindowsRouteMissing)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = waitWindowsRoute(ctx, time.Second, time.Millisecond, func(context.Context) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
}

func TestWindowsRouteQueryErrorsAreNotMissing(t *testing.T) {
	require.ErrorIs(t, windowsRouteOutcome(true, substrate.ExecResult{ExitCode: 3, ExitCodeKnown: true}, nil), errWindowsRouteMissing)
	err := windowsRouteOutcome(true, substrate.ExecResult{ExitCode: 1, ExitCodeKnown: true}, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, errWindowsRouteMissing)
	require.Error(t, windowsRouteOutcome(true, substrate.ExecResult{ExitCode: 0}, nil))
	require.NoError(t, windowsRouteOutcome(true, substrate.ExecResult{ExitCode: 0, ExitCodeKnown: true}, nil))
}
