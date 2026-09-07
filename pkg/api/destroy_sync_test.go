package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWaitForCompletionReturnsImmediatelyWhenTerminal(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := newRun("lab", "destroy", runStartOptions{})
	run.MarkFinished(nil, time.Now())
	s.jobs.remember(run)

	start := time.Now()
	err := s.runs().waitForCompletion(context.Background(), run)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 100*time.Millisecond)
}

func TestWaitForCompletionTimesOut(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := newRun("lab", "destroy", runStartOptions{}) // RunQueued, non-terminal
	s.jobs.remember(run)

	old := destroySyncTimeout
	destroySyncTimeout = 40 * time.Millisecond
	defer func() { destroySyncTimeout = old }()

	err := s.runs().waitForCompletion(context.Background(), run)
	require.Error(t, err)
}

func TestWaitForCompletionReturnsWhenRunFinishes(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := newRun("lab", "destroy", runStartOptions{})
	s.jobs.remember(run)

	done := make(chan error, 1)
	go func() {
		done <- s.runs().waitForCompletion(context.Background(), run)
	}()

	// Mark the run terminal, which should unblock the waiter.
	s.jobs.finish(run, nil)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("waitForCompletion did not return after the run finished")
	}
}
