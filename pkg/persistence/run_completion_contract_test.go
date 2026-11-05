package persistence

import (
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestWholeDestroyCompletionRequiresCleanupReceipt(t *testing.T) {
	now := time.Now().UTC()
	current := controlplane.Run{ID: "destroy", Topology: "lab", AgentID: "host", Op: "destroy", Operation: "destroy", Status: controlplane.RunRunning, QueuedAt: now.Add(-time.Minute), StartedAt: now.Add(-time.Second), Attempt: 1, LeaseOwner: "worker", LeaseUntil: now.Add(time.Minute)}
	observed := current
	observed.Status = controlplane.RunDone
	observed.EndedAt = now
	_, err := CompleteRunFromHost(current, observed, now)
	require.ErrorIs(t, err, ErrConflict)
	observed.Cleanup = &controlplane.CleanupReceipt{Version: 1, Topology: "lab", TopologyUID: "uid", AgentID: "host", RunID: "destroy", QueuedAt: current.QueuedAt, Attempt: 1, LeaseOwner: "worker", StateSerial: 1, ObservedAt: now, Empty: true}
	next, err := CompleteRunFromHost(current, observed, now)
	require.NoError(t, err)
	require.NotNil(t, next.Cleanup)
	observed.Cleanup = nil
	observed.Status = controlplane.RunFailed
	observed.Err = "provider failed"
	_, err = CompleteRunFromHost(current, observed, now)
	require.NoError(t, err)
	current.Target = "sysbox_node.web"
	observed = current
	observed.Status = controlplane.RunDone
	observed.EndedAt = now
	_, err = CompleteRunFromHost(current, observed, now)
	require.NoError(t, err)
}

func TestHostCompletionPreservesErrorCodeAndRejectsSuccessError(t *testing.T) {
	now := time.Now().UTC()
	current := controlplane.Run{ID: "apply", Topology: "lab", AgentID: "host", Op: "apply", Status: controlplane.RunRunning, QueuedAt: now.Add(-time.Minute), StartedAt: now.Add(-time.Second), Attempt: 1, LeaseOwner: "worker"}
	observed := current
	observed.Status = controlplane.RunFailed
	observed.EndedAt = now
	observed.Err = "provider read failed"
	observed.ErrorCode = "observation_unknown"
	next, err := CompleteRunFromHost(current, observed, now)
	require.NoError(t, err)
	require.Equal(t, observed.ErrorCode, next.ErrorCode)
	observed.Status = controlplane.RunDone
	observed.Err = ""
	_, err = CompleteRunFromHost(current, observed, now)
	require.ErrorIs(t, err, ErrConflict)
}
