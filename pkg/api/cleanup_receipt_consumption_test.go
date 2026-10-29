package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
)

func completedCleanupRun(t *testing.T, s *Server) controlplane.Run {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.workspaceService().bindPlacement("lab", "owner", controlplane.AgentProtocolVersion))
	queued := controlplane.Run{ID: "destroy", Topology: "lab", AgentID: "owner", Op: "destroy", Revision: "revision", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
	require.NoError(t, s.apiStore.SaveRun(ctx, queued))
	claimed, err := s.jobs.claim(queued.ID, "owner")
	require.NoError(t, err)
	receipt := &controlplane.CleanupReceipt{Version: 1, Topology: "lab", TopologyUID: "owned-topology", AgentID: "owner", RunID: claimed.ID, QueuedAt: claimed.QueuedAt, Revision: claimed.Revision, Attempt: claimed.Attempt, LeaseOwner: claimed.LeaseOwner, StateSerial: 3, ObservedAt: time.Now().UTC(), Empty: true}
	finished := *claimed
	finished.MarkFinished(nil, time.Now().UTC())
	finished.Cleanup = receipt
	fixture := prepareHostRunFixture(t, s, *claimed, receipt.TopologyUID)
	stored := fixture.complete(t, finished)
	require.NotNil(t, stored.Cleanup)
	return *stored
}

func TestCleanupReceiptSurvivesReopenAndPermitsMetadataDelete(t *testing.T) {
	for _, backend := range []string{"local", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			cfg := config.DefaultServiceConfig()
			cfg.Paths.RunsDir = t.TempDir()
			cfg.Paths.WorkspacesDir = t.TempDir()
			if backend == "sqlite" {
				cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "api.db")
			}
			first := NewServerWithConfig(cfg)
			completedCleanupRun(t, first)
			reopened := NewServerWithConfig(cfg)
			response := httptest.NewRecorder()
			reopened.ServeHTTP(response, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
			require.Equal(t, 200, response.Code)
			var evidence map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &evidence))
			require.Equal(t, "current_recorded", evidence["cleanup_receipt_status"])
			require.NotNil(t, evidence["cleanup_receipt"])
			require.Equal(t, "unknown", evidence["external_presence"], "receipt is bounded past evidence, not live global absence")
			require.NoError(t, reopened.workspaceService().Delete(context.Background(), "lab", false))
			placement, err := reopened.workspaceService().loadPlacement("lab")
			require.NoError(t, err)
			require.Nil(t, placement)
		})
	}
}

func TestCleanupReceiptCannotOutliveAdmissionOrPlacement(t *testing.T) {
	for _, change := range []string{"newer_failed_run", "same_time_run", "node_operation", "old_node_completed_late", "old_node_completion_unknown", "rebind"} {
		t.Run(change, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			ctx := context.Background()
			destroy := completedCleanupRun(t, s)
			switch change {
			case "newer_failed_run", "same_time_run":
				at := time.Now().UTC()
				if change == "same_time_run" {
					at = destroy.QueuedAt
				}
				require.NoError(t, s.apiStore.SaveRun(ctx, controlplane.Run{ID: "later", Topology: "lab", AgentID: "owner", Op: "apply", Status: controlplane.RunFailed, QueuedAt: at}))
			case "node_operation", "old_node_completed_late", "old_node_completion_unknown":
				created := time.Now().UTC()
				if change != "node_operation" {
					created = destroy.QueuedAt.Add(-time.Second)
				}
				op := controlplane.NodeOperation{ID: "later-node", Topology: "lab", AgentID: "owner", Operation: "pause", Status: controlplane.NodeOperationStatusQueued, CreatedAt: created, CommandID: "cmd", ActionID: "action", ResourceUID: "uid", Generation: 1, LeaseEpoch: 1}
				require.NoError(t, s.apiStore.CreateNodeOperation(ctx, op))
				done := op
				done.Status = controlplane.NodeOperationStatusDone
				done.EndedAt = time.Now().UTC()
				if change == "old_node_completion_unknown" {
					done.EndedAt = time.Time{}
				}
				require.NoError(t, s.apiStore.CompareAndSwapNodeOperation(ctx, op, done))
			case "rebind":
				require.NoError(t, s.workspaceService().deletePlacement(ctx, "lab"))
				require.NoError(t, s.workspaceService().bindPlacement("lab", "owner", controlplane.AgentProtocolVersion))
			}
			require.ErrorIs(t, s.workspaceService().Delete(ctx, "lab", false), persistence.ErrConflict)
			placement, err := s.workspaceService().loadPlacement("lab")
			require.NoError(t, err)
			require.NotNil(t, placement)
		})
	}
}

func TestCleanupReceiptDoesNotBypassLocalJournalOrCorruption(t *testing.T) {
	for _, fault := range []string{"journal", "corruption"} {
		t.Run(fault, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			ctx := context.Background()
			completedCleanupRun(t, s)
			if fault == "journal" {
				repo, err := s.actionRepository("lab")
				require.NoError(t, err)
				st, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				require.NoError(t, repo.CommitActionIntent(ctx, runtime.ActionRecord{ID: "pending", RunID: "unknown-run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "uid", Generation: 1}))
			} else {
				require.NoError(t, os.MkdirAll(filepath.Join(s.runsDir, "lab"), 0700))
				require.NoError(t, os.WriteFile(filepath.Join(s.runsDir, "lab", "state.json"), []byte(`{"version":1}`), 0600))
			}
			require.Error(t, s.workspaceService().Delete(ctx, "lab", false))
			placement, err := s.workspaceService().loadPlacement("lab")
			require.NoError(t, err)
			require.NotNil(t, placement)
		})
	}
}
