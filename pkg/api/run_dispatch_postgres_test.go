package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestDestroyHTTPIdempotencyIsAtomicAcrossPostgresServers(t *testing.T) {
	dsn := isolatedPostgresTestDSN(t)
	dir := t.TempDir()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = filepath.Join(dir, "runs")
	cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
	cfg.State.Backend = dsn
	servers := []*Server{NewServerWithConfig(cfg), NewServerWithConfig(cfg)}
	writeRunServiceTopology(t, servers[0], "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, servers[0].agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	const workers = 24
	responses := make(chan *httptest.ResponseRecorder, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(server *Server) {
			defer group.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
			req.Header.Set("Idempotency-Key", "destroy:cf-lab-g1")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			responses <- rec
		}(servers[i%len(servers)])
	}
	group.Wait()
	close(responses)

	var runID string
	for rec := range responses {
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		var response struct {
			RunID string `json:"run_id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		if runID == "" {
			runID = response.RunID
		}
		require.Equal(t, runID, response.RunID)
	}
	runs, err := servers[0].apiStore.LoadRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, runs, 1)
	commands, err := servers[0].apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Equal(t, "run-"+runID, commands[0].ID)
}

func TestPostgresAgentCommandRejectsStaleStatusRegression(t *testing.T) {
	dsn := isolatedPostgresTestDSN(t)
	store := newAPIStore("", dsn)
	ctx := context.Background()
	terminal := controlplane.AgentCommand{ID: "cmd-terminal", AgentID: "host-a", Type: "run_assigned", Status: controlplane.AgentCommandStatusCompleted, EndedAt: time.Now().UTC()}
	require.NoError(t, store.SaveAgentCommand(ctx, terminal))

	stale := terminal
	stale.Status = controlplane.AgentCommandStatusDelivered
	stale.EndedAt = time.Time{}
	require.NoError(t, store.SaveAgentCommand(ctx, stale))
	stale.Status = controlplane.AgentCommandStatusFailed
	require.NoError(t, store.SaveAgentCommand(ctx, stale))

	commands, err := store.ListAgentCommands(ctx, "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Equal(t, controlplane.AgentCommandStatusCompleted, commands[0].Status)
	require.False(t, commands[0].EndedAt.IsZero())
}

func TestDestroyHTTPIdempotencyRejectsFingerprintConflictAcrossPostgresServers(t *testing.T) {
	dsn := isolatedPostgresTestDSN(t)
	dir := t.TempDir()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = filepath.Join(dir, "runs")
	cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
	cfg.State.Backend = dsn
	servers := []*Server{NewServerWithConfig(cfg), NewServerWithConfig(cfg)}
	writeRunServiceTopology(t, servers[0], "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, servers[0].agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	first := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
	first.Header.Set("Idempotency-Key", "destroy:cf-lab-conflict")
	firstRecorder := httptest.NewRecorder()
	servers[0].ServeHTTP(firstRecorder, first)
	require.Equal(t, http.StatusAccepted, firstRecorder.Code, firstRecorder.Body.String())

	conflict := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy?allow_unsafe_state=true", nil)
	conflict.Header.Set("Idempotency-Key", "destroy:cf-lab-conflict")
	conflictRecorder := httptest.NewRecorder()
	servers[1].ServeHTTP(conflictRecorder, conflict)
	require.Equal(t, http.StatusConflict, conflictRecorder.Code, conflictRecorder.Body.String())
	require.Contains(t, conflictRecorder.Body.String(), "idempotency key")

	runs, err := servers[0].apiStore.LoadRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, runs, 1)
	commands, err := servers[0].apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
}

func TestPostgresRunDispatchSurvivesRestartAndIsDeliveredByReconciler(t *testing.T) {
	dsn := isolatedPostgresTestDSN(t)
	dir := t.TempDir()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = filepath.Join(dir, "runs")
	cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
	cfg.State.Backend = dsn
	bootstrap := NewServerWithConfig(cfg)
	writeRunServiceTopology(t, bootstrap, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, bootstrap.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	restarted := NewServerWithConfig(cfg)
	restarted.agentStreams().pollInterval = 10 * time.Millisecond
	streamServer := httptest.NewServer(restarted)
	defer streamServer.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+streamServer.URL[len("http"):]+"/v1/agents/host-a/commands/stream", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	creator := NewServerWithConfig(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/destroy", nil)
	req.Header.Set("Idempotency-Key", "destroy:postgres-restart-delivery")
	rec := httptest.NewRecorder()
	creator.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var response struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(readCtx)
	require.NoError(t, err)
	var command controlplane.AgentCommand
	require.NoError(t, json.Unmarshal(data, &command))
	require.Equal(t, "run-"+response.RunID, command.ID)
	require.Equal(t, controlplane.AgentCommandStatusDelivered, command.Status)
	require.NotNil(t, command.Run)
	require.Equal(t, response.RunID, command.Run.ID)
	require.Equal(t, 1, command.Attempt)
}

func TestPostgresDestroyHTTPIdempotencyAdoptsLegacyRunWithoutRedispatch(t *testing.T) {
	dsn := isolatedPostgresTestDSN(t)
	dir := t.TempDir()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = filepath.Join(dir, "runs")
	cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
	cfg.State.Backend = dsn
	assertLegacyRunDispatchAdopted(t, cfg)
}

func isolatedPostgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SYSBOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SYSBOX_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("sysbox_test_%d", time.Now().UnixNano())
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))
	t.Cleanup(func() {
		cleanup, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer cleanup.Close(context.Background())
		_, _ = cleanup.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	})
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "search_path=" + schema
}
