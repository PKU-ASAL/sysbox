package api

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestMaintenanceQuiescenceBlocksActiveAgentCommand(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	cmd := controlplane.AgentCommand{ID: "queued", AgentID: "agent", TopologyUID: st.TopologyUID, Type: "guest_execution", Status: controlplane.AgentCommandStatusQueued, CreatedAt: time.Now().UTC()}
	// Durable commands must remain visible even without an Agent registration.
	require.NoError(t, s.apiStore.SaveAgentCommand(context.Background(), cmd))
	require.ErrorIs(t, s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab"), persistence.ErrConflict)
	for _, force := range []bool{false, true} {
		require.ErrorIs(t, s.workspaceService().Delete(context.Background(), "lab", force), persistence.ErrConflict, "pending dispatch must be checked before metadata mutation")
		current, err := repo.LoadState(context.Background(), "lab")
		require.NoError(t, err)
		require.Equal(t, st.TopologyUID, current.TopologyUID)
	}
}

func TestCommandPreflightRejectsMissingOrUnavailableRegistry(t *testing.T) {
	for _, kind := range []string{"missing", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			service := s.workspaceService()
			if kind == "missing" {
				service.loadAgentCommands = nil
			} else {
				service.loadAgentCommands = func(context.Context) ([]controlplane.AgentCommand, error) { return nil, errors.New("unavailable") }
			}
			require.Error(t, service.ensureTopologyQuiescent(context.Background(), "lab"))
			require.Error(t, service.Delete(context.Background(), "lab", true))
		})
	}
}

func TestCommandActivityMatchesCurrentUIDAndRetirement(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		cmd := controlplane.AgentCommand{ID: "pending", AgentID: "unregistered", TopologyUID: uid, Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.ErrorIs(t, s.workspaceService().Delete(ctx, "lab", true), persistence.ErrConflict)
		require.NoError(t, store.DeleteAgentCommand(ctx, cmd.AgentID, cmd.ID))
		cmd.ID, cmd.TopologyUID = "foreign", "other-uid"
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		cmd.ID, cmd.TopologyUID, cmd.Status = "finished", uid, controlplane.AgentCommandStatusCompleted
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.NoError(t, s.workspaceService().checkCommandActivity(ctx, "lab"))
		require.NoError(t, s.workspaceService().Delete(ctx, "lab", true))
	})
}
