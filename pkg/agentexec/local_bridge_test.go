package agentexec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestExecutorRunsRepairThroughApplyReconcile(t *testing.T) {
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
}
`), 0o644))
	require.NoError(t, state.NewManager(statePath).Save(&state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Type:     "sysbox_kernel",
			Name:     "linux",
			Provider: "artifact",
			Instance: map[string]any{"path": filepath.Join(dir, "missing")},
		}},
	}))
	var log bytes.Buffer
	bridge := NewLocalBridge(LocalOptions{
		Topology:   topology,
		ConfigFile: hclPath,
		StatePath:  statePath,
		RunsDir:    dir,
		Log:        &log,
		Refresh:    true,
	})
	run := &controlplane.Run{ID: "run-repair", Topology: topology, Workspace: topology, Op: "repair", Operation: "repair", Status: controlplane.RunRunning}

	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)

	require.Empty(t, run.Err)
	require.Equal(t, controlplane.RunDone, run.Status)
	require.Contains(t, log.String(), "Apply complete")
	cp, err := runtime.LoadCheckpointFile(filepath.Join(dir, topology, "runs", "run-repair.checkpoint.json"))
	require.NoError(t, err)
	require.Equal(t, "repair", cp.Operation)
}

func TestLocalBridgeFilterApplyPlanByTarget(t *testing.T) {
	bridge := NewLocalBridge(LocalOptions{Target: "sysbox_node.web"})
	plan := &runtime.Plan{
		Add: []address.Address{
			{Type: "sysbox_network", Name: "shared"},
			{Type: "sysbox_node", Name: "web"},
		},
		Actions: []controlplane.PlanAction{
			{Resource: "sysbox_network.shared", Type: "sysbox_network", Name: "shared", Action: controlplane.PlanActionCreate},
			{Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Action: controlplane.PlanActionCreate},
		},
	}

	filtered, err := bridge.FilterApplyPlan(plan)
	require.NoError(t, err)
	var creates []controlplane.PlanAction
	for _, action := range filtered.Actions {
		if action.Action == controlplane.PlanActionCreate {
			creates = append(creates, action)
		}
	}
	require.Len(t, creates, 1)
	require.Equal(t, "sysbox_node.web", creates[0].Resource)
}

func TestExecuteNodeOperationRejectsMissingNode(t *testing.T) {
	runs := t.TempDir()
	topology := "lab"
	statePath := filepath.Join(runs, topology, "state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	require.NoError(t, state.NewManager(statePath).Save(&state.State{Version: state.SchemaVersion}))

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

func TestAuthorizeAgentCommandPolicy(t *testing.T) {
	denyConsole := false
	denyImport := false
	policy := config.AgentPolicyConfig{
		AllowedCommands:   []string{"run_assigned", "session_open", "node_operation"},
		AllowedWorkspaces: []string{"lab"},
		AllowedSubstrates: []string{"docker"},
		AllowConsole:      &denyConsole,
		AllowImport:       &denyImport,
	}

	require.NoError(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type: "run_assigned",
		Run:  &controlplane.Run{Workspace: "lab", Topology: "lab"},
	}))
	require.ErrorContains(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type: "run_assigned",
		Run:  &controlplane.Run{Workspace: "prod", Topology: "prod"},
	}), "workspace")
	require.ErrorContains(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type:    "session_open",
		Session: &controlplane.ConsoleSession{Workspace: "lab", Topology: "lab"},
	}), "console")
	require.ErrorContains(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type: "node_operation",
		Operation: controlplane.NodeOperation{
			Workspace: "lab",
			Topology:  "lab",
			Substrate: "firecracker",
			Operation: "pause",
		},
	}), "substrate")
	require.ErrorContains(t, authorizeAgentCommand(policy, &controlplane.AgentCommand{
		Type: "node_operation",
		Operation: controlplane.NodeOperation{
			Workspace: "lab",
			Topology:  "lab",
			Substrate: "docker",
			Operation: "import",
		},
	}), "import")
}

func TestObserveLocalBridgeReportsStateHealth(t *testing.T) {
	runs := t.TempDir()
	topology := "lab"
	statePath := filepath.Join(runs, topology, "state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	mgr := state.NewManager(statePath)
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{
		{Type: "sysbox_image", Name: "alpine", Provider: "docker", Instance: map[string]any{"repository": "alpine:latest"}},
	}}
	require.NoError(t, mgr.Save(st))

	bridge := NewLocalBridge(LocalOptions{Topology: topology, StatePath: statePath, RunsDir: runs})
	projections := Observe(context.Background(), "local", bridge)
	require.Len(t, projections, 1)
	require.Equal(t, topology, projections[0].Topology)
	require.Equal(t, "local", projections[0].AgentID)
	require.Len(t, projections[0].Resources, 1)
}

func TestLocalBridgeBuildDestroyPlanHonorsPreventDestroy(t *testing.T) {
	bridge := NewLocalBridge(LocalOptions{})
	st := &state.State{Resources: []state.Resource{
		{Type: "sysbox_node", Name: "web", Instance: map[string]any{}},
		{Type: "sysbox_node", Name: "db", Instance: map[string]any{"lifecycle_prevent_destroy": true}},
	}}

	plan, err := bridge.BuildDestroyPlan(st)
	require.NoError(t, err)
	require.Len(t, plan.Destroy, 1)
	require.Equal(t, "web", plan.Destroy[0].Name)
	require.Len(t, plan.Protected, 1)
	require.Equal(t, "db", plan.Protected[0].Name)
	require.Len(t, plan.Actions, 2)
}
