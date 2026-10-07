package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type nodeOperationProofBridge struct {
	*LocalBridge
	locked          bool
	stateReadLocked bool
	stateReads      int
}

func (b *nodeOperationProofBridge) LockTopology(string) func() {
	b.locked = true
	return func() { b.locked = false }
}
func (b *nodeOperationProofBridge) StateManager(topology string) (*state.Manager, error) {
	b.stateReads++
	b.stateReadLocked = b.locked
	return b.LocalBridge.StateManager(topology)
}

func TestNodeOperationRejectsOldImportBeforeStateAccess(t *testing.T) {
	dir := t.TempDir()
	bridge := &nodeOperationProofBridge{LocalBridge: NewLocalBridge(LocalOptions{Topology: "lab", StatePath: filepath.Join(dir, "state.json"), RunsDir: dir})}
	op := controlplane.NodeOperation{ID: "old-import", Topology: "lab", Operation: "import", Type: "sysbox_node", Name: "web", Substrate: "docker", ExternalID: "existing-container", ActionID: "old-action", ResourceUID: "old-resource", Generation: 1}
	result := NewExecutorWithBridge(bridge).ExecuteNodeOperation(context.Background(), op)
	require.Equal(t, controlplane.NodeOperationStatusFailed, result.Status)
	require.Zero(t, bridge.stateReads, "unsupported adoption must not read or mutate lifecycle state")
}

type nodeOperationProofDriver struct {
	bridge         *nodeOperationProofBridge
	calls, decoded int
	identity       substrate.OperationIdentity
	effectLocked   bool
}

func (d *nodeOperationProofDriver) MarshalProviderState(substrate.NodeHandle) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (d *nodeOperationProofDriver) UnmarshalProviderState(json.RawMessage) (any, error) {
	d.decoded++
	return nil, nil
}
func (d *nodeOperationProofDriver) Pause(ctx context.Context, _ substrate.NodeHandle) error {
	d.calls++
	d.identity, _ = substrate.OperationIdentityFromContext(ctx)
	d.effectLocked = d.bridge.locked
	return nil
}
func (d *nodeOperationProofDriver) Resume(ctx context.Context, h substrate.NodeHandle) error {
	return d.Pause(ctx, h)
}

func TestNodeOperationRejectsStaleProofBeforeProviderEffect(t *testing.T) {
	for _, operation := range []string{"pause", "resume"} {
		for _, proof := range []string{"stale_generation", "stale_uid", "missing_generation", "missing_uid", "missing_action", "current"} {
			t.Run(operation+"/"+proof, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "state.json")
				bridge := &nodeOperationProofBridge{LocalBridge: NewLocalBridge(LocalOptions{Topology: "lab", StatePath: path, RunsDir: dir})}
				provider := &nodeOperationProofDriver{bridge: bridge}
				name := fmt.Sprintf("node-proof-%d", observedResourceCounter.Add(1))
				require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: name, Version: "1", Power: provider, NodeState: provider}))
				resource := state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: name, Attributes: state.MustAttributes(map[string]any{"container_id": "node"})}
				require.NoError(t, resource.SetProviderState(json.RawMessage(`{}`)))
				saveLocalTestState(t, path, "lab", &state.State{Version: state.SchemaVersion, Resources: []state.Resource{resource}})
				mgr, err := bridge.LocalBridge.StateManager("lab")
				require.NoError(t, err)
				current, err := mgr.Load()
				require.NoError(t, err)
				current.Resources[0].Generation = 2
				require.NoError(t, mgr.Save(current))
				op := controlplane.NodeOperation{ID: "power", Topology: "lab", Node: "web", Operation: operation, Substrate: name, ActionID: "action", ResourceUID: current.Resources[0].ResourceUID, Generation: 2}
				switch proof {
				case "stale_generation":
					op.Generation = 1
				case "stale_uid":
					op.ResourceUID = "previous"
				case "missing_generation":
					op.Generation = 0
				case "missing_uid":
					op.ResourceUID = ""
				case "missing_action":
					op.ActionID = ""
				}
				result := NewExecutorWithBridge(bridge).ExecuteNodeOperation(context.Background(), op)
				if proof != "current" {
					require.Equal(t, controlplane.NodeOperationStatusFailed, result.Status)
					require.Zero(t, provider.calls)
					require.Zero(t, provider.decoded, "reject stale proof before reconstructing a new-generation handle")
				} else {
					require.Equal(t, controlplane.NodeOperationStatusDone, result.Status, result.Err)
					require.Equal(t, 1, provider.calls)
					require.Equal(t, substrate.OperationIdentity{ActionID: op.ActionID, OwnerUID: current.TopologyUID, TopologyUID: current.TopologyUID, ResourceUID: op.ResourceUID, Generation: op.Generation}, provider.identity)
					require.True(t, bridge.stateReadLocked)
					require.True(t, provider.effectLocked)
				}
				require.False(t, bridge.locked)
			})
		}
	}
}
