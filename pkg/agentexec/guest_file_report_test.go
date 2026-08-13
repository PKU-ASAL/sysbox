package agentexec

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestGuestFileReportersRetryTransientFailure(t *testing.T) {
	startAttempts, completeAttempts := 0, 0
	opts := Options{
		ReportGuestFileStartFunc: func(context.Context, string) error {
			startAttempts++
			if startAttempts < 3 {
				return errors.New("lost response")
			}
			return nil
		},
		ReportGuestFileCompleteFunc: func(context.Context, string, controlplane.GuestFileOperationCompletion) error {
			completeAttempts++
			if completeAttempts < 2 {
				return errors.New("lost response")
			}
			return nil
		},
	}
	require.NoError(t, opts.ReportGuestFileStart(context.Background(), "op"))
	require.NoError(t, opts.ReportGuestFileComplete(context.Background(), "op", controlplane.GuestFileOperationCompletion{}))
	require.Equal(t, 3, startAttempts)
	require.Equal(t, 2, completeAttempts)
}
