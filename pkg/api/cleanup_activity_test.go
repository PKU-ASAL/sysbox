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
	"github.com/stretchr/testify/require"
)

func TestCleanupInspectionReportsUnresolvedConsoleAndCommand(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		sess := controlplane.ConsoleSession{ID: "lost", Topology: "lab", Workspace: "different-workspace", AgentID: "missing-agent", Status: controlplane.ConsoleSessionStatusLost, CreatedAt: time.Now().Add(-time.Minute), EndedAt: time.Now(), Err: "private-console-error", RequestedBy: "private-actor"}
		require.NoError(t, store.SaveConsoleSession(ctx, sess))
		cmd := controlplane.AgentCommand{ID: "pending", AgentID: "unregistered-agent", Type: "guest_execution", TopologyUID: uid, Status: controlplane.AgentCommandStatusQueued, ExecutionRequest: controlplane.GuestExecutionRequest{Argv: []string{"private-argument"}, Environment: map[string]string{"TOKEN": "private-secret"}}}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		foreign := cmd
		foreign.ID, foreign.TopologyUID = "foreign", "different-uid"
		require.NoError(t, store.SaveAgentCommand(ctx, foreign))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out struct {
			Active []struct{ ID, Kind, Status string } `json:"active_operations"`
			Agents []string                            `json:"agent_ids"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.Len(t, out.Active, 2)
		require.Equal(t, "agent_command", out.Active[0].Kind)
		require.Equal(t, "pending", out.Active[0].ID)
		require.Equal(t, "console_session", out.Active[1].Kind)
		require.Equal(t, "lost", out.Active[1].Status)
		require.ElementsMatch(t, []string{"missing-agent", "unregistered-agent"}, out.Agents)
		for _, secret := range []string{"private-console-error", "private-actor", "private-argument", "private-secret"} {
			require.NotContains(t, rec.Body.String(), secret)
		}
	})
}

type unavailableCleanupActivity struct {
	controlstore.Store
	kind   string
	called *bool
}

func (s unavailableCleanupActivity) ListConsoleSessions(ctx context.Context, w string) ([]controlplane.ConsoleSession, error) {
	if s.kind == "console" {
		*s.called = true
		return nil, errors.New("private-storage-location")
	}
	return s.Store.ListConsoleSessions(ctx, w)
}
func (s unavailableCleanupActivity) ListAgentCommands(ctx context.Context, a string) ([]controlplane.AgentCommand, error) {
	if s.kind == "command" {
		*s.called = true
		return nil, errors.New("private-storage-location")
	}
	return s.Store.ListAgentCommands(ctx, a)
}

func TestCleanupInspectionFailsWhenActivityEnumerationUnavailable(t *testing.T) {
	for _, kind := range []string{"console", "command"} {
		t.Run(kind, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			called := false
			s.apiStore = unavailableCleanupActivity{Store: s.apiStore, kind: kind, called: &called}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
			require.Equal(t, 503, rec.Code)
			require.True(t, called, "failure must originate from the requested activity source")
			require.NotContains(t, rec.Body.String(), "private-storage-location")
		})
	}
}

func TestCleanupReceiptReportsNewActivityBlockers(t *testing.T) {
	for _, kind := range []string{"console", "command", "command_missing_uid"} {
		t.Run(kind, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			completedCleanupRun(t, s)
			uid := grantTestTopologyUID(t, s, "lab")
			if kind == "console" {
				require.NoError(t, s.apiStore.SaveConsoleSession(context.Background(), controlplane.ConsoleSession{ID: "lost", Topology: "lab", Status: controlplane.ConsoleSessionStatusLost, CreatedAt: time.Now(), EndedAt: time.Now()}))
			} else {
				cmd := controlplane.AgentCommand{ID: "pending", AgentID: "agent", TopologyUID: uid, Type: "gc_delete", Status: controlplane.AgentCommandStatusQueued}
				if kind == "command_missing_uid" {
					cmd.TopologyUID = ""
					cmd.Session = &controlplane.ConsoleSession{Topology: "lab"}
				}
				require.NoError(t, s.apiStore.SaveAgentCommand(context.Background(), cmd))
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/topologies/lab/cleanup-inspection", nil))
			require.Equal(t, 200, rec.Code, rec.Body.String())
			var out map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			want := "blocked_by_console_activity"
			if kind != "console" {
				want = "blocked_by_agent_command"
			}
			require.Equal(t, want, out["cleanup_receipt_status"])
			require.NotNil(t, out["cleanup_receipt"], "historical receipt remains visible, but no longer sufficient")
		})
	}
}
