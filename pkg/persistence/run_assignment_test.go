package persistence_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// TestAssignRunPreservesAnAdmittedRequestFingerprint pins the identity split:
// the durable request fingerprint is the run's immutable input-hydration
// authority, while the run-dispatch marker only names the command. Assigning a
// restarted incarnation must not erase the admitted request.
func TestAssignRunPreservesAnAdmittedRequestFingerprint(t *testing.T) {
	queued := controlplane.Run{
		ID: "request-1", Topology: "lab", Op: "apply", Operation: "apply",
		Status: controlplane.RunQueued, OperationKey: "op",
		QueuedAt:           time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		RequestFingerprint: "apply-request:original",
	}
	assigned := queued
	assigned.MarkAssigned("host-a", queued.QueuedAt.Add(time.Second))

	stored, err := persistence.AssignRun(queued, assigned, "run-dispatch:command-a:apply:host-a")
	require.NoError(t, err)
	require.Equal(t, "apply-request:original", stored.RequestFingerprint)
	require.Equal(t, controlplane.RunAssigned, stored.Status)
	require.Equal(t, "host-a", stored.AgentID)
}

// TestAssignRunBindsLegacyRunsToTheirDispatch: a run with no request binding
// (the pre-keyed interactive path) still acquires the dispatch identity.
func TestAssignRunBindsLegacyRunsToTheirDispatch(t *testing.T) {
	queued := controlplane.Run{
		ID: "legacy-1", Topology: "lab", Op: "apply", Operation: "apply",
		Status: controlplane.RunQueued, OperationKey: "op",
		QueuedAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}
	assigned := queued
	assigned.MarkAssigned("host-a", queued.QueuedAt.Add(time.Second))

	stored, err := persistence.AssignRun(queued, assigned, "run-dispatch:command-a:apply:host-a")
	require.NoError(t, err)
	require.Equal(t, "run-dispatch:command-a:apply:host-a", stored.RequestFingerprint)
}
