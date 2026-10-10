package agentexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestLocalBridgeParentRunRequiresPersistedIdentityAndLoadsCheckpoint(t *testing.T) {
	dir := t.TempDir()
	b := NewLocalBridge(LocalOptions{Topology: "lab", StatePath: filepath.Join(dir, "state.json"), RunsDir: dir})
	repo, err := b.ActionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	resource := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: st.TopologyUID, ResourceUID: "web", Generation: 1}
	st.Resources = append(st.Resources, resource)
	mgr, err := b.StateManager("lab")
	require.NoError(t, err)
	require.NoError(t, mgr.Save(st))
	queued := time.Now().UTC()
	action := runtime.ActionRecord{ID: "action", Topology: "lab", RunID: "parent", RunQueuedAt: queued, RunRevision: "revision", Resource: resource.Address, Action: controlplane.PlanActionReset, Status: runtime.ActionIntentCommitted, Phase: "accepted", OwnerUID: st.TopologyUID, ResourceUID: resource.ResourceUID, Generation: 2, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Candidate: &resource}
	committer := repo.(runtime.ResetIntentCommitter)
	require.NoError(t, committer.CommitResetIntents(context.Background(), []runtime.ActionRecord{action}))
	checkpointPath := b.CheckpointFile("lab", "parent")
	recorder := runtime.NewFileRecorder(checkpointPath, "parent", "lab")
	recorder.SetStateSerialBefore(st.Meta.Serial)
	require.NoError(t, recorder.Begin("reset", &runtime.Plan{Actions: []controlplane.PlannedChange{{Address: resource.Address, Action: controlplane.PlanActionReset}}}))
	_, err = b.ParentRun(context.Background(), "parent")
	require.ErrorContains(t, err, "return evidence unavailable")
	recorder.Finish(nil)
	b.Finish(&controlplane.Run{ID: "parent", Topology: "lab", Op: "reset", QueuedAt: queued, Revision: "revision"}, nil)
	parent, err := b.ParentRun(context.Background(), "parent")
	require.NoError(t, err)
	require.Equal(t, queued, parent.QueuedAt)
	require.Equal(t, "revision", parent.Revision)
	loader := b.CheckpointStore().(runtime.CheckpointLoader)
	cp, err := loader.LoadCheckpoint(context.Background(), "lab", "parent")
	require.NoError(t, err)
	require.Equal(t, "parent", cp.RunID)
	require.NoError(t, b.VerifyResetContinuation(context.Background(), &controlplane.Run{ID: "child", Topology: "lab", ParentID: "parent"}, parent))
	raw, err := os.ReadFile(checkpointPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(checkpointPath, append(raw, '\n'), 0600))
	_, err = b.ParentRun(context.Background(), "parent")
	require.ErrorContains(t, err, "checkpoint changed")
	_, err = loader.LoadCheckpoint(context.Background(), "lab", "../../escape")
	require.Error(t, err)
}
