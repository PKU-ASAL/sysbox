package hostsupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func TestFailedChildrenRequireVerifiedStopBeforeTerminalEvidence(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "unknown"}[confirmed], func(t *testing.T) {
			dispatcher, req, base := taskFixture(t)
			attachRunChild(dispatcher, base, confirmed)
			children := &RunChildren{Dispatcher: dispatcher}
			parent, cancel := context.WithCancel(context.Background())
			_, err := children.Start(parent, testRef(req), req)
			require.NoError(t, err)
			cancel()
			run := &controlplane.Run{ID: "run", Status: controlplane.RunDone, AgentID: "agent", Op: "apply"}
			err = FinishRunChildren(parent, children, run)
			var stopped *quiescentRunFailure
			if !confirmed {
				require.False(t, errors.As(err, &stopped))
				return
			}
			require.ErrorAs(t, err, &stopped)
			var result controlplane.Run
			require.NoError(t, json.Unmarshal(stopped.evidence, &result))
			require.Equal(t, controlplane.RunFailed, result.Status)
			require.False(t, result.EndedAt.IsZero())
			require.WithinDuration(t, time.Now(), result.EndedAt, time.Second)
		})
	}
}

func TestConfirmedFailureRetainsProofAcrossJournalOpenFailure(t *testing.T) {
	c, req, grant := runFixture(t)
	service, stop := context.WithCancel(context.Background())
	defer stop()
	entered, release := make(chan struct{}), make(chan struct{})
	errorsSeen := make(chan error, 1)
	c.OnExecutionError = func(_ RunRequest, err error) {
		select {
		case errorsSeen <- err:
		default:
		}
	}
	_, err := c.StartAsync(context.Background(), service, req, grant, func(context.Context) (json.RawMessage, error) {
		close(entered)
		<-release
		raw, err := json.Marshal(controlplane.Run{ID: req.RunID, AgentID: c.AgentID, Op: req.Action, Status: controlplane.RunFailed, EndedAt: time.Now()})
		if err != nil {
			return nil, err
		}
		return nil, &quiescentRunFailure{evidence: raw}
	})
	require.NoError(t, err)
	<-entered
	// A group-writable trust root must be refused, then retried after repaired.
	require.NoError(t, os.Chmod(c.Root, 0770))
	close(release)
	select {
	case <-errorsSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("missing persistence diagnostic")
	}
	require.NoError(t, os.Chmod(c.Root, 0700))
	require.Eventually(t, func() bool {
		result, err := c.ObserveAsync(context.Background(), req, grant)
		return err == nil && result.Status == "completed"
	}, 3*time.Second, 10*time.Millisecond)
}

func TestConfirmedFailurePersistsAfterCancellation(t *testing.T) {
	c, req, grant := runFixture(t)
	service, stop := context.WithCancel(context.Background())
	defer stop()
	entered := make(chan struct{})
	_, err := c.StartAsync(context.Background(), service, req, grant, func(ctx context.Context) (json.RawMessage, error) {
		close(entered)
		<-ctx.Done()
		raw, err := json.Marshal(controlplane.Run{ID: req.RunID, AgentID: c.AgentID, Op: req.Action, Status: controlplane.RunFailed, EndedAt: time.Now()})
		if err != nil {
			return nil, err
		}
		return nil, &quiescentRunFailure{evidence: raw}
	})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback not started")
	}
	require.NoError(t, c.Cancel(context.Background(), req, grant))
	require.Eventually(t, func() bool {
		observation, err := c.ObserveAsync(context.Background(), req, grant)
		return err == nil && observation.Status == "completed"
	}, time.Second, time.Millisecond)
}
