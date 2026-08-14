package agentexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/state"
)

func TestInventoryMarksObservedTopologyAvailable(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	workspacePath := filepath.Join(root, "lab.sysbox.hcl")
	require.NoError(t, state.NewManager(statePath).Save(&state.State{Version: state.SchemaVersion}))
	require.NoError(t, os.WriteFile(workspacePath, []byte(""), 0o600))
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: workspacePath, StatePath: statePath, RunsDir: root})

	inventory := Inventory(context.Background(), Options{ID: "host-a"}, bridge)

	require.Len(t, inventory.Topologies, 1)
	require.Equal(t, "lab", inventory.Topologies[0].Topology)
	require.True(t, inventory.Topologies[0].Available)
}

func TestInventoryDoesNotClaimSharedStateWithoutLocalWorkspace(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	require.NoError(t, state.NewManager(statePath).Save(&state.State{Version: state.SchemaVersion}))
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: filepath.Join(root, "missing.sysbox.hcl"), StatePath: statePath, RunsDir: root})

	inventory := Inventory(context.Background(), Options{ID: "host-b"}, bridge)

	require.Len(t, inventory.Topologies, 1)
	require.False(t, inventory.Topologies[0].Available)
}
