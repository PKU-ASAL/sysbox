package hostexec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestExecutorRepairObservesBeforeApprovingPlan(t *testing.T) {
	dir := t.TempDir()
	topology := "lab"
	kernelPath := filepath.Join(dir, "vmlinux")
	require.NoError(t, os.WriteFile(kernelPath, []byte("kernel"), 0o644))
	hclPath := filepath.Join(dir, "field.sysbox.hcl")
	statePath := filepath.Join(dir, "state.json")
	require.NoError(t, os.WriteFile(hclPath, []byte(`
resource "sysbox_kernel" "linux" {
  substrate = "docker"
  source = "`+kernelPath+`"
  architecture = "amd64"
}
`), 0o644))
	saveLocalTestState(t, statePath, topology, &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:    address.Resource("sysbox_kernel", "linux"),
			Driver:     "artifact",
			Attributes: map[string]any{"path": filepath.Join(dir, "missing")},
		}},
	})
	var log bytes.Buffer
	bridge := NewLocalBridge(LocalOptions{
		Topology:   topology,
		ConfigFile: hclPath,
		StatePath:  statePath,
		RunsDir:    dir,
		Log:        &log,
		BeforeApply: func(plan *runtime.Plan) error {
			require.True(t, plan.HasChanges(), "approval must see observed replacement")
			return nil
		},
	})
	run := &controlplane.Run{ID: "run-repair", Topology: topology, Workspace: topology, Op: "repair", Operation: "repair", Status: controlplane.RunRunning}

	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)

	require.Empty(t, run.Err)
	require.Equal(t, controlplane.RunDone, run.Status)
	cp, err := runtime.LoadCheckpointFile(filepath.Join(dir, topology, "runs", "run-repair.checkpoint.json"))
	require.NoError(t, err)
	require.Equal(t, "repair", cp.Operation)
}

func TestExecuteNodeOperationRejectsMissingNode(t *testing.T) {
	runs := t.TempDir()
	topology := "lab"
	statePath := filepath.Join(runs, topology, "state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	saveLocalTestState(t, statePath, topology, &state.State{Version: state.SchemaVersion})

	bridge := NewLocalBridge(LocalOptions{Topology: topology, StatePath: statePath, RunsDir: runs})
	exec := NewExecutorWithBridge(bridge)
	op := exec.ExecuteNodeOperation(context.Background(), controlplane.NodeOperation{
		ID:        "op1",
		Topology:  topology,
		Operation: "pause",
		Node:      "missing",
	})
	require.Equal(t, "failed", op.Status)
	require.Contains(t, op.Err, "not found")
}

func TestObserveLocalBridgeReportsStateHealth(t *testing.T) {
	runs := t.TempDir()
	topology := "lab"
	statePath := filepath.Join(runs, topology, "state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{
		{Address: address.Resource("sysbox_image", "alpine"), Driver: "docker", Attributes: map[string]any{"repository": "alpine:latest"}},
	}}
	saveLocalTestState(t, statePath, topology, st)

	bridge := NewLocalBridge(LocalOptions{Topology: topology, StatePath: statePath, RunsDir: runs})
	_, projections, err := CollectHostObservation(context.Background(), HostObservationOptions{ID: "local"}, testHostObservationSource{bridge})
	require.NoError(t, err)
	require.Len(t, projections, 1)
	require.Equal(t, topology, projections[0].Topology)
	require.Equal(t, "local", projections[0].AgentID)
	require.Len(t, projections[0].Resources, 1)
}
