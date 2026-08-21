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

func TestInventoryReportsHostCapacityAndConfiguredArtifacts(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "kernel")
	require.NoError(t, os.WriteFile(artifact, []byte("kernel"), 0o600))
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: filepath.Join(root, "missing.hcl"), StatePath: filepath.Join(root, "state.json"), RunsDir: root})

	inventory := Inventory(context.Background(), Options{ID: "host-a", CapacityPath: root, ArtifactPaths: []string{artifact, filepath.Join(root, "missing")}}, bridge)

	require.Greater(t, inventory.Resources.CPU, int64(0))
	require.Greater(t, inventory.Resources.MemoryBytes, int64(0))
	require.Greater(t, inventory.Resources.DiskBytes, int64(0))
	require.Len(t, inventory.Artifacts, 2)
	require.True(t, inventory.Artifacts[0].Available)
	require.False(t, inventory.Artifacts[1].Available)
}
