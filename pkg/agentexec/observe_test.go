package agentexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/state"
)

type testHostObservationSource struct{ Bridge }

func (s testHostObservationSource) ObservationTopologies(ctx context.Context) ([]string, error) {
	return s.Topologies(ctx), nil
}

type blockedObservationBackend struct {
	state.Backend
	entered chan struct{}
	release chan struct{}
}

func (b blockedObservationBackend) Load(ctx context.Context) ([]byte, error) {
	close(b.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return nil, context.Canceled
	}
}

type blockedObservationBridge struct {
	Bridge
	manager *state.Manager
}

func (b blockedObservationBridge) StateManager(string) (*state.Manager, error) { return b.manager, nil }
func (blockedObservationBridge) Topologies(context.Context) []string           { return []string{"lab"} }

func TestObservationCancelsBlockedStateRead(t *testing.T) {
	backend := blockedObservationBackend{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(backend.release)
	bridge := blockedObservationBridge{manager: state.NewManagerWithBackend(backend)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		CollectHostObservation(ctx, HostObservationOptions{ID: "agent"}, testHostObservationSource{bridge})
		close(done)
	}()
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("observer did not read state")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer discarded cancellation while waiting for state")
	}
}

func TestInventoryMarksObservedTopologyAvailable(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	workspacePath := filepath.Join(root, "lab.sysbox.hcl")
	saveLocalTestState(t, statePath, "lab", &state.State{Version: state.SchemaVersion})
	require.NoError(t, os.WriteFile(workspacePath, []byte(""), 0o600))
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: workspacePath, StatePath: statePath, RunsDir: root})

	inventory, _, err := CollectHostObservation(context.Background(), HostObservationOptions{ID: "host-a"}, testHostObservationSource{bridge})
	require.NoError(t, err)

	require.Len(t, inventory.Topologies, 1)
	require.Equal(t, "lab", inventory.Topologies[0].Topology)
	require.True(t, inventory.Topologies[0].Available)
}

func TestInventoryDoesNotClaimSharedStateWithoutLocalWorkspace(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	saveLocalTestState(t, statePath, "lab", &state.State{Version: state.SchemaVersion})
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: filepath.Join(root, "missing.sysbox.hcl"), StatePath: statePath, RunsDir: root})

	inventory, _, err := CollectHostObservation(context.Background(), HostObservationOptions{ID: "host-b"}, testHostObservationSource{bridge})
	require.NoError(t, err)

	require.Len(t, inventory.Topologies, 1)
	require.False(t, inventory.Topologies[0].Available)
}

func TestInventoryReportsHostCapacityAndConfiguredArtifacts(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "kernel")
	require.NoError(t, os.WriteFile(artifact, []byte("kernel"), 0o600))
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: filepath.Join(root, "missing.hcl"), StatePath: filepath.Join(root, "state.json"), RunsDir: root})

	inventory, _, err := CollectHostObservation(context.Background(), HostObservationOptions{ID: "host-a", CapacityPath: root, ArtifactPaths: []string{artifact, filepath.Join(root, "missing")}}, testHostObservationSource{bridge})
	require.NoError(t, err)

	require.Greater(t, inventory.Resources.CPU, int64(0))
	require.Greater(t, inventory.Resources.MemoryBytes, int64(0))
	require.Greater(t, inventory.Resources.DiskBytes, int64(0))
	require.Len(t, inventory.Artifacts, 2)
	require.True(t, inventory.Artifacts[0].Available)
	require.False(t, inventory.Artifacts[1].Available)
}
