package hostexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type resetPlanningDriver struct{ driver.Node }

func (resetPlanningDriver) DecodeProviderConfig(hcl.Body, *hcl.EvalContext) (any, error) {
	return nil, nil
}
func (resetPlanningDriver) Dependencies(any) substrate.ProviderDeps { return substrate.ProviderDeps{} }

func TestLocalResetExecutionPreservesConfiguredTarget(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "planning", Version: "1", Node: resetPlanningDriver{}}))
	for _, explicit := range []string{"", "sysbox_node.first"} {
		t.Run("explicit="+explicit, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "field.sysbox.hcl")
			statePath := filepath.Join(dir, "state.json")
			require.NoError(t, os.WriteFile(path, []byte(`resource "sysbox_image" "base" {
 substrate = "planning"
 kind = "oci"
 source = "fixture"
 architecture = "amd64"
 guest_family = "linux"
}
resource "sysbox_node" "first" {
 substrate = "planning"
 image = sysbox_image.base.id
}
resource "sysbox_node" "second" {
 substrate = "planning"
 image = sysbox_image.base.id
}
`), 0600))
			saveLocalTestState(t, statePath, "lab", &state.State{Version: state.SchemaVersion, Resources: []state.Resource{
				{Address: address.Resource("sysbox_image", "base"), Attributes: state.MustAttributes(map[string]any{"image_id": "base-id", "kind": "oci", "source": "fixture", "sha256": "sha256:" + strings.Repeat("a", 64), "architecture": "amd64", "guest_family": "linux"})},
				{Address: address.Resource("sysbox_node", "first")}, {Address: address.Resource("sysbox_node", "second")},
			}})
			var approved []string
			bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: path, StatePath: statePath, RunsDir: dir, Log: io.Discard, Target: "sysbox_node.second", BeforeReset: func(p *runtime.Plan) error {
				for _, a := range p.Actions {
					approved = append(approved, a.Address.String())
				}
				return errors.New("stop before provider effects")
			}})
			run := &controlplane.Run{ID: "reset", Topology: "lab", Op: "reset", Target: explicit}
			NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)
			require.Contains(t, run.Err, "stop before provider effects")
			expected := "sysbox_node.second"
			if explicit != "" {
				expected = explicit
			}
			require.Equal(t, []string{expected}, approved)
		})
	}
}

func TestTargetedApplyRetryRetainsLocalTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "field.sysbox.hcl")
	handler := &agentObservedResource{kind: fmt.Sprintf("targeted_retry_resource_%d", observedResourceCounter.Add(1))}
	runtime.RegisterResourceHandler(handler)
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`resource %q "selected" {}
resource %q "unrelated" {}
`, handler.kind, handler.kind)), 0600))
	var selected []string
	bridge := NewLocalBridge(LocalOptions{
		Topology: "lab", ConfigFile: path, StatePath: filepath.Join(dir, "state.json"), RunsDir: dir, Log: io.Discard,
		Target: handler.kind + ".selected",
		BeforeApply: func(plan *runtime.Plan) error {
			for _, action := range plan.Actions {
				if action.Action != model.PlanActionNoop {
					selected = append(selected, action.Address.String())
				}
			}
			return errors.New("inspect approved scope")
		},
	})
	run := &controlplane.Run{ID: "retry", ParentID: "prior", Topology: "lab", Op: "apply"}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)
	require.Contains(t, run.Err, "inspect approved scope")
	require.Equal(t, []string{handler.kind + ".selected"}, selected)
}

func TestLifecyclePanicDoesNotCertifyRunCompletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte(""), 0600))
	completions := 0
	bridge := NewLocalBridge(LocalOptions{
		Topology: "lab", ConfigFile: path, StatePath: filepath.Join(dir, "state.json"), RunsDir: dir, Log: io.Discard,
		BeforeApply: func(*runtime.Plan) error { panic("approval callback interrupted") },
		AfterRun:    func(*controlplane.Run) { completions++ },
	})
	run := &controlplane.Run{ID: "apply", Topology: "lab", Op: "apply", Status: controlplane.RunRunning}
	require.PanicsWithValue(t, "approval callback interrupted", func() { NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run) })
	require.Zero(t, completions, "panic cannot certify a result")
	require.Equal(t, controlplane.RunRunning, run.Status)
	require.True(t, run.EndedAt.IsZero())
}
