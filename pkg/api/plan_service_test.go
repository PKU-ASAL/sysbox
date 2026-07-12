package api

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
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
