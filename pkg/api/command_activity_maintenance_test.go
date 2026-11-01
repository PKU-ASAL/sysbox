package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
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
	require.ErrorIs(t, s.workspaceService().EnsureTopologyQuiescent(context.Background(), "lab"), persistence.ErrConflict)
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
				service.LoadAgentCommands = nil
			} else {
				service.LoadAgentCommands = func(context.Context) ([]controlplane.AgentCommand, error) { return nil, errors.New("unavailable") }
			}
			require.Error(t, service.EnsureTopologyQuiescent(context.Background(), "lab"))
			require.Error(t, service.Delete(context.Background(), "lab", true))
		})
	}
}

func TestCommandActivityMatchesCurrentUIDAndRetirement(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
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
		require.NoError(t, s.workspaceService().CheckCommandActivity(ctx, "lab"))
		require.NoError(t, s.workspaceService().Delete(ctx, "lab", true))
	})
}

func TestCommandWithoutUIDCannotHideItsTopologyActivity(t *testing.T) {
	for _, kind := range []string{"run", "node", "guest", "file", "console"} {
		t.Run(kind, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			cmd := controlplane.AgentCommand{ID: "pending", AgentID: "unregistered", Status: controlplane.AgentCommandStatusQueued}
			switch kind {
			case "run":
				cmd.Type, cmd.Run = "run_assigned", &controlplane.Run{Topology: "lab"}
			case "node":
				cmd.Type, cmd.Operation = "node_operation", controlplane.NodeOperation{Topology: "lab"}
			case "guest":
				cmd.Type, cmd.Execution = "guest_execution", &controlplane.GuestExecution{Topology: "lab"}
			case "file":
				cmd.Type, cmd.FilePut = "guest_file_put", &controlplane.GuestFilePut{Topology: "lab"}
			case "console":
				cmd.Type, cmd.Session = "console_session", &controlplane.ConsoleSession{Topology: "lab"}
			}
			require.NoError(t, s.apiStore.SaveAgentCommand(context.Background(), cmd))
			require.ErrorIs(t, s.workspaceService().CheckCommandActivity(context.Background(), "lab"), persistence.ErrConflict)
			require.NoError(t, s.workspaceService().CheckCommandActivity(context.Background(), "other"))
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
			require.Equal(t, 200, rec.Code)
			var inspection struct {
				Active []struct{ ID, Kind string } `json:"active_operations"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &inspection))
			require.Len(t, inspection.Active, 1)
			require.Equal(t, "agent_command", inspection.Active[0].Kind)
			require.Equal(t, cmd.ID, inspection.Active[0].ID)
			require.False(t, controlstore.CommandBlocksTopology(controlplane.AgentCommand{TopologyUID: "old", Run: &controlplane.Run{Topology: "lab"}, Status: controlplane.AgentCommandStatusQueued}, "lab", "new"))
			cmd.Status = controlplane.AgentCommandStatusCompleted
			require.False(t, controlstore.CommandBlocksTopology(cmd, "lab", "new"))
		})
	}
}
