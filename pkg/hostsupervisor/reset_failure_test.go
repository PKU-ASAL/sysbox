package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestReturnedResetFailureRequiresKnownActionPhases(t *testing.T) {
	now := time.Now().UTC()
	run := controlplane.Run{ID: "reset", Topology: "lab", Op: "reset", Status: controlplane.RunFailed, QueuedAt: now, EndedAt: now, Err: "interrupted"}
	for _, phase := range []string{"accepted", "prepared", "destroy_returned", "destroyed", "preparing", "destroying", "applying", ""} {
		t.Run(phase, func(t *testing.T) {
			actions := []runtime.ActionRecord{{ID: "a", Topology: "lab", RunID: run.ID, RunQueuedAt: now, Action: controlplane.PlanActionReset, Status: runtime.ActionUncertain, Phase: phase}}
			err := FinishReturnedResetFailure(context.Background(), &RunChildren{}, &run, &run, actions)
			var stopped *quiescentRunFailure
			if phase == "accepted" || phase == "prepared" || phase == "destroy_returned" || phase == "destroyed" {
				require.ErrorAs(t, err, &stopped)
			} else {
				require.NotErrorAs(t, err, &stopped)
			}
		})
	}
	var stopped *quiescentRunFailure
	require.NotErrorAs(t, FinishReturnedResetFailure(context.Background(), &RunChildren{}, &run, &run, nil), &stopped)
	actions := []runtime.ActionRecord{{ID: "a", Topology: "lab", RunID: run.ID, RunQueuedAt: now, Action: controlplane.PlanActionReset, Status: runtime.ActionUncertain, Phase: "destroyed"}}
	require.NotErrorAs(t, FinishReturnedResetFailure(context.Background(), &RunChildren{unknownAdmission: true}, &run, &run, actions), &stopped)
	foreign := run
	foreign.QueuedAt = now.Add(time.Second)
	require.NotErrorAs(t, FinishReturnedResetFailure(context.Background(), &RunChildren{}, &run, &foreign, actions), &stopped)
}

func TestReturnedResetFailurePublishesSignedTerminalEvidence(t *testing.T) {
	c, req, grant := runFixture(t, "reset")
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c.HostKey = key
	reported := make(chan controlplane.Run, 1)
	c.ReportRunID = func(_ context.Context, id string, receipt controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
		require.Equal(t, req.RunID, id)
		require.NoError(t, controlplane.VerifyRunStopReceipt(pub, receipt, raw))
		var run controlplane.Run
		require.NoError(t, json.Unmarshal(raw, &run))
		reported <- run
		return nil
	}
	now := time.Now().UTC()
	run := controlplane.Run{ID: req.RunID, Topology: req.Topology, Op: "reset", AgentID: c.AgentID, Status: controlplane.RunFailed, QueuedAt: now, EndedAt: now, Err: "checkpoint unavailable"}
	actions := []runtime.ActionRecord{{ID: "a", Topology: run.Topology, RunID: run.ID, RunQueuedAt: now, Action: controlplane.PlanActionReset, Status: runtime.ActionUncertain, Phase: "destroyed"}}
	release := make(chan struct{})
	_, err = c.StartAsync(context.Background(), context.Background(), req, grant, func(ctx context.Context) (json.RawMessage, error) {
		<-release
		return nil, FinishReturnedResetFailure(ctx, &RunChildren{}, &run, &run, actions)
	})
	require.NoError(t, err)
	active := c.findActive(grant)
	require.NotNil(t, active)
	close(release)
	select {
	case result := <-reported:
		require.Equal(t, controlplane.RunFailed, result.Status)
		require.Equal(t, run.Err, result.Err)
		require.True(t, result.Recoverable)
	case <-time.After(5 * time.Second):
		t.Fatal("returned safe reset did not report completion")
	}
	<-active.done
}
