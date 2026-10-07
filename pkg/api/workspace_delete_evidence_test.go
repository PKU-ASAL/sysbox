package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestRemoteMetadataDeleteRequiresExplicitForceEvenAfterDestroy(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	w := s.workspaceService()
	require.NoError(t, w.bindPlacement("lab", "owner", controlplane.AgentProtocolVersion))
	require.ErrorIs(t, w.Delete(ctx, "lab", false), persistence.ErrConflict)
	placement, err := w.loadPlacement("lab")
	require.NoError(t, err)
	require.NotNil(t, placement)
	require.NoError(t, s.apiStore.SaveRun(ctx, controlplane.Run{ID: "destroyed", Topology: "lab", AgentID: "owner", Op: "destroy", Status: controlplane.RunDone, QueuedAt: time.Now().UTC()}))
	require.ErrorIs(t, w.Delete(ctx, "lab", false), persistence.ErrConflict, "terminal run is not a durable empty resource receipt")
	require.NoError(t, w.Delete(ctx, "lab", true))
	placement, err = w.loadPlacement("lab")
	require.NoError(t, err)
	require.Nil(t, placement)
}

func TestMetadataForceCannotDeleteWhileLifecycleOperationActive(t *testing.T) {
	for _, kind := range []string{"run", "node_operation"} {
		t.Run(kind, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			ctx := context.Background()
			w := s.workspaceService()
			require.NoError(t, w.bindPlacement("lab", "owner", controlplane.AgentProtocolVersion))
			if kind == "run" {
				require.NoError(t, s.apiStore.SaveRun(ctx, controlplane.Run{ID: "active", Topology: "lab", AgentID: "owner", Op: "apply", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}))
			} else {
				require.NoError(t, s.apiStore.CreateNodeOperation(ctx, controlplane.NodeOperation{ID: "active", Topology: "lab", AgentID: "owner", Operation: "pause", Status: controlplane.NodeOperationStatusQueued, CreatedAt: time.Now().UTC(), CommandID: "cmd", ActionID: "action", ResourceUID: "uid", Generation: 1, LeaseEpoch: 1}))
			}
			for _, force := range []bool{false, true} {
				require.ErrorIs(t, w.Delete(ctx, "lab", force), persistence.ErrConflict)
			}
			placement, err := w.loadPlacement("lab")
			require.NoError(t, err)
			require.NotNil(t, placement)
		})
	}
}

func TestMetadataDeleteSerializesWithAdmission(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	unlock := s.jobs.lockTopology("lab")
	result := make(chan error, 1)
	go func() { result <- s.workspaceService().Delete(ctx, "lab", true) }()
	select {
	case err := <-result:
		unlock()
		t.Fatalf("metadata deletion bypassed admission lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	err := s.apiStore.SaveRun(ctx, controlplane.Run{ID: "admitted", Topology: "lab", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()})
	unlock()
	require.NoError(t, err)
	select {
	case err := <-result:
		require.ErrorIs(t, err, persistence.ErrConflict)
	case <-time.After(time.Second):
		t.Fatal("metadata deletion did not finish after admission unlock")
	}
}
