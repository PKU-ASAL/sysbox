package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestUnclaimedDeliveryCannotReplayClaimedOrGrantedRun(t *testing.T) {
	now := time.Now().UTC()
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "host", Op: "destroy", QueuedAt: now.Add(-time.Hour), Status: controlplane.RunAssigned}
	cmd := controlplane.AgentCommand{ID: "cmd", Type: "run_assigned", AgentID: "host", ActionID: "run", TopologyUID: "uid", Run: &run, Status: controlplane.AgentCommandStatusAcknowledged, Attempt: 1, LeaseOwner: "old", LeaseUntil: now.Add(-time.Second)}
	record := &CommandRecord{Command: cmd}
	got, err := RecoverUnclaimedDelivery(&run, record, nil, now)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, controlplane.AgentCommandStatusQueued, got.Command.Status)
	require.Equal(t, 1, got.Command.Attempt)
	for _, status := range []controlplane.RunStatus{controlplane.RunRunning, controlplane.RunDone, controlplane.RunFailed} {
		copy := run
		copy.Status = status
		got, err = RecoverUnclaimedDelivery(&copy, record, nil, now)
		require.NoError(t, err)
		require.Nil(t, got)
	}
	copy := run
	copy.Attempt = 1
	got, err = RecoverUnclaimedDelivery(&copy, record, nil, now)
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = RecoverUnclaimedDelivery(&run, record, []byte("broken"), now)
	require.Error(t, err)
	require.Nil(t, got)
}
