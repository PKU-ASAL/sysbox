package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestPlanServiceComputeAndValidateStoredPlan(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)

	plan, err := s.plans().ComputeStoredPlan(context.Background(), "lab")
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanStatusPlanned, plan.Status)
	require.NotEmpty(t, plan.ID)
	require.NotEmpty(t, plan.Revision)
	require.NotEmpty(t, plan.Actions)
	require.NotEmpty(t, plan.Fingerprint.ConfigSHA256)
	require.Equal(t, "sysbox_network@builtin-v1", plan.Fingerprint.Drivers["sysbox_network.lab"])

	require.NoError(t, s.apiStore.SavePlan(context.Background(), plan))
	got, err := s.plans().ValidateStoredPlanForApply(context.Background(), "lab", plan.ID, plan.StateSerial)
	require.NoError(t, err)
	require.Equal(t, plan.ID, got.ID)
}

func TestPlanFingerprintInputsPrefersReobservedArtifactDigestOverState(t *testing.T) {
	addr := address.Resource("sysbox_image", "base")
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: addr, Attributes: map[string]any{"sha256": "sha256:old"}}}}
	inputs := planFingerprintInputs(nil, st, 0, graph.New(), map[string]string{addr.String(): "sha256:new"})
	require.Equal(t, "sha256:new", inputs.Artifacts[addr.String()])
}

func TestPlanServiceRejectsStoredPlanAfterArtifactContentChanges(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	kernel := filepath.Join(t.TempDir(), "vmlinux")
	require.NoError(t, os.WriteFile(kernel, []byte("first"), 0o600))
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_kernel" "linux" {
  substrate = "firecracker"
  source = "`+kernel+`"
  architecture = "amd64"
}`)

	plan, err := s.plans().ComputeStoredPlan(context.Background(), "lab")
	require.NoError(t, err)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, plan.Fingerprint.Artifacts["sysbox_kernel.linux"])
	require.NoError(t, s.apiStore.SavePlan(context.Background(), plan))
	require.NoError(t, os.WriteFile(kernel, []byte("second"), 0o600))

	_, err = s.plans().ValidateStoredPlanForApply(context.Background(), "lab", plan.ID, plan.StateSerial)
	require.ErrorContains(t, err, "stale plan: artifacts changed")
}

func TestPlanServiceRejectsStaleStoredPlan(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)

	plan, err := s.plans().ComputeStoredPlan(context.Background(), "lab")
	require.NoError(t, err)
	require.NoError(t, s.apiStore.SavePlan(context.Background(), plan))

	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	require.NoError(t, mgr.Save(&state.State{Version: state.SchemaVersion}))

	_, err = s.plans().ValidateStoredPlanForApply(context.Background(), "lab", plan.ID, plan.StateSerial+1)
	require.ErrorContains(t, err, "stale")
}

func TestPlanServiceRejectsPlanAfterConfigurationChanges(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" { cidr = "10.77.0.0/24" }`)
	plan, err := s.plans().ComputeStoredPlan(context.Background(), "lab")
	require.NoError(t, err)
	require.NoError(t, s.apiStore.SavePlan(context.Background(), plan))

	require.NoError(t, os.WriteFile(s.workspaceService().HCLFile("lab"), []byte(`resource "sysbox_network" "lab" { cidr = "10.88.0.0/24" }`), 0o600))
	_, err = s.plans().ValidateStoredPlanForApply(context.Background(), "lab", plan.ID, plan.StateSerial)
	require.ErrorContains(t, err, "configuration changed")
}
