package controlstore

import (
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestInitialHistoryOnlyAllowsSafeAssignmentTransition(t *testing.T) {
	now := time.Now().UTC()
	run := controlplane.Run{ID: "current", Topology: "lab", Op: "apply", Revision: "rev", QueuedAt: now, AgentID: "host", Status: controlplane.RunRunning}
	for _, scenario := range []string{"queued", "wrong-agent", "changed-revision", "claimed-queued", "older-incarnation"} {
		t.Run(scenario, func(t *testing.T) {
			prior := run
			prior.Status = controlplane.RunQueued
			prior.AgentID = ""
			switch scenario {
			case "wrong-agent":
				prior.AgentID = "other"
			case "changed-revision":
				prior.Revision = "different"
			case "claimed-queued":
				prior.Attempt = 1
			case "older-incarnation":
				prior.QueuedAt = now.Add(-time.Second)
			}
			err := validateInitialHistory([]controlplane.Run{prior, run}, run, nil)
			if scenario == "queued" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, persistence.ErrConflict)
			}
		})
	}
}
func TestRecreatedInitialHistoryNeedsTerminalPreDeletionRecords(t *testing.T) {
	boundary := time.Now().UTC()
	receipt := &persistence.LifecycleDeletionReceipt{TopologyUID: "old", Serial: 5, DeletedAt: boundary, Empty: true}
	current := controlplane.Run{ID: "current", Topology: "lab", Op: "apply", Revision: "rev", QueuedAt: boundary.Add(time.Second), AgentID: "host", Status: controlplane.RunRunning}
	old := controlplane.Run{ID: "old", Topology: "lab", Op: "apply", QueuedAt: boundary.Add(-time.Minute), EndedAt: boundary.Add(-time.Second), Status: controlplane.RunDone}
	require.NoError(t, validateInitialHistory([]controlplane.Run{old, current}, current, receipt))
	for _, mutate := range []func(*controlplane.Run){func(r *controlplane.Run) { r.Status = controlplane.RunRunning }, func(r *controlplane.Run) { r.EndedAt = time.Time{} }, func(r *controlplane.Run) { r.EndedAt = boundary }, func(r *controlplane.Run) { r.QueuedAt = boundary }} {
		changed := old
		mutate(&changed)
		require.Error(t, validateInitialHistory([]controlplane.Run{changed, current}, current, receipt))
	}
	current.QueuedAt = boundary
	require.Error(t, validateInitialHistory([]controlplane.Run{old, current}, current, receipt))
}
