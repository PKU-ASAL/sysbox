package agentexec

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestGuestCompletionReportSurvivesExecutionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reported := false
	r := newCommandRunner(nil, Options{RequestTimeout: time.Second, ReportGuestExecutionCompleteFunc: func(reportCtx context.Context, id string, result controlplane.GuestExecutionCompletion) error {
		reported = true
		require.NoError(t, reportCtx.Err(), "execution cancellation must not suppress its result report")
		deadline, ok := reportCtx.Deadline()
		require.True(t, ok, "detached reporting must remain bounded")
		require.WithinDuration(t, time.Now().Add(time.Second), deadline, 200*time.Millisecond)
		require.Equal(t, "operation", id)
		require.Equal(t, "owner", result.LeaseOwner)
		require.Equal(t, 2, result.Attempt)
		return nil
	}})
	// With no supervisor configured, the real execution branch returns an
	// error without starting an effect, then must still report that outcome.
	r.Execute(ctx, &controlplane.AgentCommand{ID: "command", Type: "guest_execution", Execution: &controlplane.GuestExecution{ID: "operation"}, LeaseOwner: "owner", Attempt: 2}, nil)
	require.True(t, reported)
}

func TestGuestCompletionReportHasIndependentTimeout(t *testing.T) {
	reported := false
	r := newCommandRunner(nil, Options{RequestTimeout: 10 * time.Millisecond, ReportGuestExecutionCompleteFunc: func(ctx context.Context, _ string, _ controlplane.GuestExecutionCompletion) error {
		reported = true
		_, ok := ctx.Deadline()
		require.True(t, ok)
		<-ctx.Done()
		return ctx.Err()
	}})
	var terminal controlplane.AgentCommandEvent
	r.Execute(context.Background(), &controlplane.AgentCommand{ID: "command", Type: "guest_execution", Execution: &controlplane.GuestExecution{ID: "operation"}}, func(e controlplane.AgentCommandEvent) { terminal = e })
	require.True(t, reported)
	require.Equal(t, "failed", terminal.Status)
	require.Equal(t, "guest execution completion report failed", terminal.Message)
	require.Contains(t, terminal.Error, context.DeadlineExceeded.Error())
}
