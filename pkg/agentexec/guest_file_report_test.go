package agentexec

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestFileReportersRetryTransientFailure(t *testing.T) {
	startAttempts := 0
	opts := Options{
		ReportGuestFileStartFunc: func(context.Context, string) error {
			startAttempts++
			if startAttempts < 3 {
				return errors.New("lost response")
			}
			return nil
		},
	}
	require.NoError(t, opts.ReportGuestFileStart(context.Background(), "op"))
	require.Equal(t, 3, startAttempts)
}
