package hostexec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// resetAssertionDriver is the smallest substrate that can complete one reset:
// no attachments, no guest network init, no provisioners. Its only purpose is
// to let the executor reach the post-reset assertion step.
type resetAssertionDriver struct {
	substrate.BaseSubstrate
	driver.NIC
	created map[string]bool
	deleted map[string]bool
}

func (*resetAssertionDriver) Capabilities() substrate.Capabilities { return substrate.Capabilities{} }
func (*resetAssertionDriver) CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, errors.New("unexpected create")
}
func (*resetAssertionDriver) StartNode(context.Context, substrate.NodeHandle) error { return nil }
func (*resetAssertionDriver) StopNode(context.Context, substrate.NodeHandle) error  { return nil }
func (*resetAssertionDriver) DestroyNode(context.Context, substrate.NodeHandle) error {
	return errors.New("unexpected destroy")
}
func (*resetAssertionDriver) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}
func (d *resetAssertionDriver) ObserveNode(_ context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	if d.deleted[h.ID] {
		return substrate.NodeObservation{Status: substrate.NodeStatusMissing, ExternalID: h.ID}, nil
	}
	return substrate.NodeObservation{Exists: true, Running: true, Healthy: true, Status: substrate.NodeStatusRunning, ExternalID: h.ID}, nil
}
func (*resetAssertionDriver) PrepareReset(_ context.Context, req substrate.ResetRequest) (substrate.ResetHandle, error) {
	return substrate.ResetHandle{Provider: req.Current.ID}, nil
}
func (d *resetAssertionDriver) DestroyReset(_ context.Context, h substrate.ResetHandle) error {
	d.deleted[h.Provider.(string)] = true
	return nil
}
func (d *resetAssertionDriver) ApplyReset(_ context.Context, h substrate.ResetHandle) (substrate.NodeHandle, error) {
	old := h.Provider.(string)
	d.created[old] = true
	return substrate.NodeHandle{ID: "new-" + old}, nil
}
func (d *resetAssertionDriver) ObserveReset(_ context.Context, h substrate.ResetHandle) (substrate.ResetObservation, error) {
	old := h.Provider.(string)
	if d.created[old] {
		return substrate.ResetObservation{Phase: substrate.ResetPhaseComplete, Converged: true, NewExternalID: "new-" + old}, nil
	}
	return substrate.ResetObservation{Phase: substrate.ResetPhasePrepared}, nil
}
func (*resetAssertionDriver) CleanupReset(context.Context, substrate.ResetHandle) error { return nil }
func (*resetAssertionDriver) MarshalResetHandle(h substrate.ResetHandle) (json.RawMessage, error) {
	return json.Marshal(h.Provider)
}
func (*resetAssertionDriver) UnmarshalResetHandle(raw json.RawMessage) (substrate.ResetHandle, error) {
	var old string
	err := json.Unmarshal(raw, &old)
	return substrate.ResetHandle{Provider: old}, err
}

// A reset that leaves the topology healthy must still evaluate the scenario's
// checks. Before this, only apply/repair recorded an assertion, so a reset run
// left Asserted=Unknown and Ready could never return.
func TestResetEvaluatesChecksAndRecordsAssertion(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	d := &resetAssertionDriver{created: map[string]bool{}, deleted: map[string]bool{}}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{
		Name: "reset-assert", Version: "1", Node: d, NodeState: d, NIC: d, Reset: d,
	}))

	dir := t.TempDir()
	path := filepath.Join(dir, "field.sysbox.hcl")
	statePath := filepath.Join(dir, "state.json")
	require.NoError(t, os.WriteFile(path, []byte(`resource "sysbox_image" "base" {
  substrate = "reset-assert"
  kind = "rootfs"
  source = "/base"
  architecture = "amd64"
  guest_family = "linux"
}
resource "sysbox_node" "first" {
  substrate = "reset-assert"
  image = sysbox_image.base.id
}
check "first_answers" {
  data "sysbox_exec" "probe" {
    node = sysbox_node.first.id
    argv = ["true"]
  }
  assert {
    condition     = data.sysbox_exec.probe.exit_code == 0
    error_message = "first node must answer"
  }
}
`), 0600))
	saveLocalTestState(t, statePath, "lab", &state.State{Version: state.SchemaVersion, Resources: []state.Resource{
		{Address: address.Resource("sysbox_image", "base"), Driver: "reset-assert", Attributes: state.MustAttributes(map[string]any{
			"image_id": "base", "kind": "rootfs", "source": "/base",
			"sha256":       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"architecture": "amd64", "guest_family": "linux",
		})},
		{Address: address.Resource("sysbox_node", "first"), Driver: "reset-assert", ExternalID: "old-first", Status: state.ResourcePresent,
			Attributes: state.MustAttributes(map[string]any{"guest_family": "linux"})},
	}})

	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: path, StatePath: statePath, RunsDir: dir, Log: io.Discard})
	run := &controlplane.Run{ID: "reset", Topology: "lab", Op: "reset", Status: controlplane.RunRunning}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)

	require.Empty(t, run.Err)
	require.Equal(t, controlplane.RunDone, run.Status)
	require.NotNil(t, run.Assertion, "reset must record the evaluated checks")
	// The fixture registers no guest-exec capability, so the probe cannot pass;
	// what matters is that reset evaluated it at all.
	require.Equal(t, []string{"first_answers"}, run.Assertion.FailedChecks)
	require.NotEmpty(t, run.Assertion.Message)
}
