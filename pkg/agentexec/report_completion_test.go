package agentexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type retryReporter struct {
	failures int
	calls    int
}

func (r *retryReporter) ReportRunComplete(context.Context, *controlplane.Run, controlplane.Projection) error {
	r.calls++
	if r.calls <= r.failures {
		return errors.New("transient report failure")
	}
	return nil
}

func withShortRetryInterval(t *testing.T) {
	t.Helper()
	previous := reportRunCompleteRetryInterval
	reportRunCompleteRetryInterval = time.Millisecond
	t.Cleanup(func() { reportRunCompleteRetryInterval = previous })
}

func TestReportRunCompleteRetriesUntilSuccess(t *testing.T) {
	withShortRetryInterval(t)
	reporter := &retryReporter{failures: 1}
	err := reportRunCompleteWithRetry(context.Background(), reporter, &controlplane.Run{ID: "run-1"}, controlplane.Projection{})
	require.NoError(t, err)
	require.Equal(t, 2, reporter.calls)
}

func TestReportRunCompleteStopsOnContextCancel(t *testing.T) {
	withShortRetryInterval(t)
	reporter := &retryReporter{failures: 100}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := reportRunCompleteWithRetry(ctx, reporter, &controlplane.Run{ID: "run-1"}, controlplane.Projection{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, reporter.calls)
}
