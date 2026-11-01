package persistence

import (
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestLocalRecoveryCommitCannotAdoptRemoteOrChangedRun(t *testing.T) {
	now := time.Now().UTC()
	expected := controlplane.Run{ID: "run", Topology: "lab", Revision: "revision", QueuedAt: now.Add(-time.Hour), Status: controlplane.RunFailed, Recoverable: true}
	action := runtime.ActionRecord{RunID: expected.ID, Topology: expected.Topology, RunRevision: expected.Revision, RunQueuedAt: expected.QueuedAt}
	require.NoError(t, ValidateLocalRecoveryCommit(expected, []controlplane.Run{expected}, action, now))
	for _, change := range []func(*controlplane.Run){
		func(r *controlplane.Run) { r.AgentID = "remote-agent" },
		func(r *controlplane.Run) { r.Attempt++ },
		func(r *controlplane.Run) { r.LeaseOwner = "other" },
		func(r *controlplane.Run) { r.QueuedAt = now },
		func(r *controlplane.Run) { r.Status = controlplane.RunDone },
		func(r *controlplane.Run) { r.Recoverable = false },
		func(r *controlplane.Run) { r.LeaseUntil = now.Add(time.Minute) },
	} {
		current := expected
		change(&current)
		require.ErrorIs(t, ValidateLocalRecoveryCommit(expected, []controlplane.Run{current}, action, now), ErrConflict)
	}
	peer := controlplane.Run{ID: "peer", Topology: "lab", Status: controlplane.RunAssigned}
	require.ErrorIs(t, ValidateLocalRecoveryCommit(expected, []controlplane.Run{expected, peer}, action, now), ErrConflict)
	wrong := action
	wrong.RunQueuedAt = now
	require.ErrorIs(t, ValidateLocalRecoveryCommit(expected, []controlplane.Run{expected}, wrong, now), ErrConflict)
}
