package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/workspace"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceCatalogIncludesActiveSnapshotOnlyAdmission(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	run := controlplane.Run{ID: "admitted", Topology: "lab", Status: controlplane.RunAssigned, Op: "apply", Revision: "pinned", QueuedAt: time.Now().UTC()}
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	listed, err := s.workspaceService().List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "lab", listed[0].Name)
	require.False(t, listed[0].HasHCL)
	require.False(t, listed[0].HasState)
	require.Equal(t, "admitted", listed[0].ActiveRunID)
	require.Equal(t, "pinned", listed[0].ActiveRevision)
	info, err := s.workspaceService().Get(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, "admitted", info.ActiveRunID)
	run.Status = controlplane.RunDone
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	listed, err = s.workspaceService().List(ctx)
	require.NoError(t, err)
	require.Empty(t, listed, "retained terminal history must not resurrect deleted metadata")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
	require.Equal(t, 200, rec.Code)
	var evidence map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &evidence))
	require.Empty(t, evidence["active_operations"], "old assigned history is not an active operation")
}

func TestWorkspaceCatalogRetainsPlacedRemoteTopologyUntilMetadataRemoval(t *testing.T) {
	for _, backend := range []string{"local", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			cfg := config.DefaultServiceConfig()
			cfg.Paths.RunsDir = t.TempDir()
			cfg.Paths.WorkspacesDir = t.TempDir()
			if backend == "sqlite" {
				cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "api.db")
			}
			s := NewServerWithConfig(cfg)
			ctx := context.Background()
			require.NoError(t, s.workspaceService().BindPlacement("lab", "owner", controlplane.AgentProtocolVersion))
			require.NoError(t, s.apiStore.SaveRun(ctx, controlplane.Run{ID: "applied", Topology: "lab", AgentID: "owner", Op: "apply", Status: controlplane.RunDone, QueuedAt: time.Now().UTC(), EndedAt: time.Now().UTC()}))
			listed, err := s.workspaceService().List(ctx)
			require.NoError(t, err)
			require.Len(t, listed, 1)
			require.Equal(t, "owner", listed[0].PlacementAgentID)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab", nil))
			require.Equal(t, 200, rec.Code)
			var info workspace.Info
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
			require.Equal(t, controlplane.TopologyPhase("unknown"), info.Status.Phase)
			require.False(t, info.HasState)
			require.Error(t, s.workspaceService().Delete(ctx, "lab", false))
			require.NoError(t, s.workspaceService().Delete(ctx, "lab", true))
			listed, err = s.workspaceService().List(ctx)
			require.NoError(t, err)
			require.Empty(t, listed)
		})
	}
}
