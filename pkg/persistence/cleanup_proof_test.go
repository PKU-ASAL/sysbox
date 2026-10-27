package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCleanupProofRejectsPendingIntentInEmptyState(t *testing.T) {
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	run := controlplane.Run{ID: "destroy", Topology: "lab", Op: "destroy", AgentID: "agent", QueuedAt: time.Now().UTC(), StartedAt: time.Now().UTC(), Attempt: 1, LeaseOwner: "lease", Status: controlplane.RunRunning}
	_, err = r.ProveCleanup(ctx, run)
	require.ErrorContains(t, err, "missing persisted state")
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, state.NewManagerWithBackend(r.StateBackend("lab")).Save(st))
	proof, err := r.ProveCleanup(ctx, run)
	require.NoError(t, err)
	require.True(t, proof.Empty)
	require.NotEmpty(t, proof.TopologyUID)
	a := runtime.ActionRecord{ID: "pending", RunID: "apply", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "web", Generation: 1, StateSerial: proof.StateSerial, ExpectedSerial: proof.StateSerial}
	require.NoError(t, r.CommitActionIntent(ctx, a))
	_, err = r.ProveCleanup(ctx, run)
	require.Error(t, err, "empty state cannot conceal an unfinished external effect")
}
