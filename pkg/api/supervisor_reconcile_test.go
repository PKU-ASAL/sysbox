package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// A run that crashed after creating a resource but before recording it in state
// leaves an orphan that only checkpoint recovery can find. Today that recovery
// happens only when someone calls POST /v1/runs/{id}/recover by hand — nothing
// in the background loop looks, so an orphan stays invisible until someone
// remembers to look.
//
// The supervisor already walks every topology on an interval; it is the natural
// place to close the loop. A crashed run is recoverable when its checkpoint
// journal still holds an unrecorded state patch; the supervisor should reconcile
// it so the orphan is adopted back into state without human intervention.
func TestSupervisorReconcilesRecoverableRun(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "mixed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "mixed", "field.sysbox.hcl"), []byte(""), 0o644))
	writeState(t, runs, "mixed", &state.State{Version: state.SchemaVersion})

	s := NewServer(runs, workspaces)

	// A crashed run: failed and recoverable.
	run := s.jobs.start("mixed", "apply")
	run.Status = controlplane.RunFailed
	run.Recoverable = true
	s.jobs.replace(run)

	// Its checkpoint journal still holds an unrecorded state patch — the node
	// was created in the outside world but never recorded in state.
	cp := runtime.OperationCheckpoint{
		RunID:    run.ID,
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
	require.NoError(t, s.apiStore.SaveCheckpoint(context.Background(), "mixed", run.ID, cp))

	supervisor := newSupervisor(s, time.Minute)
	require.NoError(t, supervisor.ScanTopology(context.Background(), "mixed"))

	// The orphan was adopted: state now holds the node the crashed run never
	// recorded, without anyone calling the recover endpoint.
	mgr, err := s.stateManager("mixed")
	require.NoError(t, err)
	st, err := mgr.Load()
	require.NoError(t, err)
	res := st.FindResource(address.Resource("sysbox_node", "web"))
	require.NotNil(t, res, "the supervisor must reconcile a recoverable run's journal automatically")
	require.Equal(t, "abc", res.ContainerID())

	// And it is surfaced in the snapshot, not silently done.
	snap, err := s.loadHealthSnapshot("mixed")
	require.NoError(t, err)
	require.Contains(t, snap.Recovered, run.ID)
}

// A running run must never be reconciled: only terminal, recoverable runs have
// a journal that needs adoption. A run still in flight owns its state.
func TestSupervisorDoesNotReconcileRunningRun(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	workspaces := filepath.Join(dir, "workspaces")
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "mixed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "mixed", "field.sysbox.hcl"), []byte(""), 0o644))
	writeState(t, runs, "mixed", &state.State{Version: state.SchemaVersion})

	s := NewServer(runs, workspaces)

	run := s.jobs.start("mixed", "apply")
	run.Status = controlplane.RunRunning
	run.Recoverable = true // a running run should never carry this, but belt-and-braces
	s.jobs.replace(run)

	cp := runtime.OperationCheckpoint{
		RunID:    run.ID,
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
	require.NoError(t, s.apiStore.SaveCheckpoint(context.Background(), "mixed", run.ID, cp))

	supervisor := newSupervisor(s, time.Minute)
	require.NoError(t, supervisor.ScanTopology(context.Background(), "mixed"))

	mgr, err := s.stateManager("mixed")
	require.NoError(t, err)
	st, err := mgr.Load()
	require.NoError(t, err)
	require.Nil(t, st.FindResource(address.Resource("sysbox_node", "web")),
		"a running run must not be reconciled; its journal is still being written")
}
