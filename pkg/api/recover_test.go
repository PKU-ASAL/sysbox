package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestRecoverCheckpointRejectsBlindStatePatchReplay(t *testing.T) {
	dir := t.TempDir()
	cp := runtime.OperationCheckpoint{
		RunID:    "run-1",
		Topology: "mixed",
		StatePatches: []runtime.StatePatch{{
			Resource: "sysbox_node.web",
			Action:   model.PlanActionCreate,
			Op:       runtime.StatePatchUpsert,
			State: &runtime.StateResourceLog{
				Type: "sysbox_node", Name: "web",
				Provider: "docker",
				Instance: map[string]any{"container_id": "abc"},
			},
		}},
	}
	mgr := state.NewManager(filepath.Join(dir, "state.json"))
	store := &controlstore.Local{RunsDir: dir}
	require.NoError(t, store.SaveCheckpoint(context.Background(), "mixed", "run-1", cp))
	report, err := reconcileCheckpointJournal(context.Background(), store, "mixed", "run-1", mgr, "test")
	require.ErrorIs(t, err, runtime.ErrConvergenceUnsupported)
	require.Nil(t, report)

	st, err := mgr.Load()
	require.NoError(t, err)
	res := st.FindResource(address.Resource("sysbox_node", "web"))
	require.Nil(t, res)

	cpPtr, err := store.LoadCheckpoint(context.Background(), "mixed", "run-1")
	require.NoError(t, err)
	require.False(t, cpPtr.StatePatches[0].Recorded)
}

func TestAdoptStateResourceDropsLegacyProviderExtra(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	runtime.AdoptStateResource(st, runtime.StateResourceLog{
		Type: "sysbox_node", Name: "vm",
		Provider: "firecracker",
		Instance: map[string]any{
			"container_id":    "sysbox-vm",
			"provider_extra":  `{"vm_dir":"/tmp/sysbox-vm"}`,
			"desired_hash_v2": "hash",
		},
	}, "")

	res := st.FindResource(address.Resource("sysbox_node", "vm"))
	require.NotNil(t, res)
	require.Equal(t, "sysbox-vm", res.ContainerID())
	providerState, err := res.ProviderState()
	require.NoError(t, err)
	require.Empty(t, providerState)
	require.NotContains(t, res.Attributes, "provider_extra")
}
