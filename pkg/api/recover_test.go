package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestRecoverCandidateRequiresSupportedDoneUnrecordedStateResource(t *testing.T) {
	step := runtime.OperationStep{
		Kind:          "resource",
		Provider:      "docker",
		Status:        runtime.OperationDone,
		StateResource: &runtime.StateResourceLog{Type: "sysbox_node", Name: "web"},
	}
	require.True(t, recoverCandidate(step))

	step.StateRecorded = true
	require.False(t, recoverCandidate(step))

	step.StateRecorded = false
	step.StateResource = nil
	require.False(t, recoverCandidate(step))

	step.StateResource = &runtime.StateResourceLog{Type: "sysbox_node", Name: "vm"}
	step.Provider = "firecracker"
	require.True(t, recoverCandidate(step))

	step.Provider = "network"
	require.True(t, recoverCandidate(step))

	step.Provider = "libvirt"
	step.StateResource = &runtime.StateResourceLog{Type: "sysbox_unknown", Name: "thing", Provider: "libvirt"}
	require.False(t, recoverCandidate(step))
}

func TestRecoverCheckpointReplaysStatePatch(t *testing.T) {
	dir := t.TempDir()
	cp := runtime.OperationCheckpoint{
		RunID:    "run-1",
		Topology: "mixed",
		StatePatches: []runtime.StatePatch{{
			Resource: "sysbox_node.web",
			Action:   controlplane.PlanActionCreate,
			Op:       runtime.StatePatchUpsert,
			State: &runtime.StateResourceLog{
				Type:     "sysbox_node",
				Name:     "web",
				Provider: "docker",
				Instance: map[string]any{"container_id": "abc"},
			},
		}},
	}
	mgr := state.NewManager(filepath.Join(dir, "state.json"))
	store := &localAPIStore{runsDir: dir}
	require.NoError(t, store.SaveCheckpoint(context.Background(), "mixed", "run-1", cp))
	report, err := reconcileCheckpointJournal(context.Background(), store, "mixed", "run-1", mgr, "test")
	require.NoError(t, err)
	require.Len(t, report.Recovered, 1)
	require.Equal(t, "replayed_state_patch", report.Recovered[0].Status)

	st, err := mgr.Load()
	require.NoError(t, err)
	res := st.FindResource("sysbox_node", "web")
	require.NotNil(t, res)
	require.Equal(t, "abc", res.ContainerID())

	cpPtr, err := store.LoadCheckpoint(context.Background(), "mixed", "run-1")
	require.NoError(t, err)
	require.True(t, cpPtr.StatePatches[0].Recorded)
}

func TestAdoptFirecrackerStateResourceKeepsProviderExtra(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	runtime.AdoptStateResource(st, runtime.StateResourceLog{
		Type:     "sysbox_node",
		Name:     "vm",
		Provider: "firecracker",
		Instance: map[string]any{
			"container_id":    "sysbox-vm",
			"provider_extra":  `{"vm_dir":"/tmp/sysbox-vm"}`,
			"desired_hash_v2": "hash",
		},
	}, "")

	res := st.FindResource("sysbox_node", "vm")
	require.NotNil(t, res)
	require.Equal(t, "sysbox-vm", res.ContainerID())
	require.Equal(t, `{"vm_dir":"/tmp/sysbox-vm"}`, res.ProviderExtra())
}
