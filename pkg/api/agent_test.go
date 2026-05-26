package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestAgentRegistry(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"id":"local"`)

	body := bytes.NewBufferString(`{
  "id": "host-a",
  "capabilities": ["docker", "network", "kvm"],
  "labels": {"role": "lab"},
  "version": "dev"
}`)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents", body))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"id":"host-a"`)
	require.Contains(t, rec.Body.String(), `"status":"online"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents/host-a", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"version":"dev"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/heartbeat", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"online"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Len(t, listed.Agents, 2)
	require.ElementsMatch(t, []string{"host-a", "local"}, []string{listed.Agents[0].ID, listed.Agents[1].ID})

	stored, err := s.apiStore.GetAgent(context.Background(), "host-a")
	require.NoError(t, err)
	require.Equal(t, "host-a", stored.ID)
	require.Equal(t, controlplane.AgentProtocolVersion, stored.Protocol)
	require.Empty(t, stored.AuthSecret)
}

func TestAgentCommandWebSocketReceivesAssignedRunAndReportsAck(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	server := httptest.NewServer(s)
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):] + "/v1/agents/host-a/commands/stream"
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	eventCh := make(chan controlplane.AgentCommandEvent, 1)
	go func() {
		var event controlplane.AgentCommandEvent
		_, data, err := conn.Read(context.Background())
		if err == nil {
			var cmd controlplane.AgentCommand
			require.NoError(t, json.Unmarshal(data, &cmd))
			require.Equal(t, "run_assigned", cmd.Type)
			require.NotEmpty(t, cmd.ID)
			require.NotNil(t, cmd.Run)
			event = controlplane.AgentCommandEvent{CommandID: cmd.ID, Type: cmd.Type, Status: "ack"}
			raw, _ := json.Marshal(event)
			_ = conn.Write(context.Background(), websocket.MessageText, raw)
		}
		eventCh <- event
	}()

	run := s.jobs.start("mixed", "apply")
	require.NoError(t, s.dispatchRun(context.Background(), run, []string{"docker"}))

	select {
	case event := <-eventCh:
		require.Equal(t, "ack", event.Status)
		require.NotEmpty(t, event.CommandID)
		require.Eventually(t, func() bool {
			events := s.agents.ListCommandEvents("host-a")
			return len(events) == 1 && events[0].CommandID == event.CommandID && events[0].Status == "ack"
		}, time.Second, 10*time.Millisecond)

		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents/host-a/command-events", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), event.CommandID)
		require.Contains(t, rec.Body.String(), `"status":"ack"`)

		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents/host-a/commands", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"status":"acked"`)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for command websocket")
	}
}

func TestAgentCommandWebSocketReplaysPendingCommand(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	cmd, err := s.publishAgentCommand(context.Background(), "host-a", controlplane.AgentCommand{
		Type: "run_assigned",
		Run:  &controlplane.Run{ID: "run-1", Topology: "mixed", Workspace: "mixed", AgentID: "host-a"},
	})
	require.NoError(t, err)

	server := httptest.NewServer(s)
	defer server.Close()
	wsURL := "ws" + server.URL[len("http"):] + "/v1/agents/host-a/commands/stream"
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, data, err := conn.Read(context.Background())
	require.NoError(t, err)
	var got controlplane.AgentCommand
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, cmd.ID, got.ID)
	require.Equal(t, "delivered", got.Status)
	require.Equal(t, controlplane.AgentProtocolVersion, got.Protocol)
	require.Equal(t, 1, got.Attempt)
	require.NotEmpty(t, got.LeaseOwner)
	require.False(t, got.LeaseUntil.IsZero())

	server2 := httptest.NewServer(s)
	defer server2.Close()
	conn2, _, err := websocket.Dial(context.Background(), "ws"+server2.URL[len("http"):]+"/v1/agents/host-a/commands/stream", nil)
	require.NoError(t, err)
	defer conn2.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _, err = conn2.Read(ctx)
	require.Error(t, err)
}

func TestAgentCommandCancelPublishesCancelCommand(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	cmd, err := s.publishAgentCommand(context.Background(), "host-a", controlplane.AgentCommand{
		Type: "node_operation",
		Operation: controlplane.NodeOperation{
			ID:        "op-1",
			Topology:  "mixed",
			Workspace: "mixed",
			Operation: "pause",
		},
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/commands/"+cmd.ID+"/cancel", nil))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"type":"cancel_command"`)
	require.Contains(t, rec.Body.String(), cmd.ID)
}

func TestAgentDisableAndQuarantineBlockSchedulingAndStream(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/v1/agents/host-a", bytes.NewBufferString(`{"disabled":true,"reason":"maintenance"}`))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"disabled"`)

	stored, err := s.apiStore.GetAgent(context.Background(), "host-a")
	require.NoError(t, err)
	require.True(t, stored.Disabled)
	require.Equal(t, "disabled", stored.Status)

	agent, err := s.selectAgent(context.Background(), []string{"docker"})
	require.NoError(t, err)
	require.NotEqual(t, "host-a", agent.ID)

	server := httptest.NewServer(s)
	defer server.Close()
	_, resp, err := websocket.Dial(context.Background(), "ws"+server.URL[len("http"):]+"/v1/agents/host-a/commands/stream", nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAgentSignatureRequiredWhenSecretRegistered(t *testing.T) {
	secret := "agent-secret"
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{
		ID:           "host-a",
		Status:       "online",
		AuthSecret:   secret,
		SecretHash:   agent.SecretHash(secret),
		Capabilities: []string{"docker"},
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/heartbeat", bytes.NewBufferString(`{"id":"host-a"}`))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/heartbeat", bytes.NewBufferString(`{"id":"host-a"}`))
	require.NoError(t, agent.SignRequest(req, "host-a", secret, time.Now()))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "auth_secret")
}

func TestAgentProtocolMismatchRejected(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agents", bytes.NewBufferString(`{"id":"host-a","protocol":"agent.v0"}`))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "unsupported agent protocol")
}

func TestAgentCommandLeaseExpiresAndRedelivers(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()
	cmd := controlplane.AgentCommand{
		ID:        "cmd-1",
		AgentID:   "host-a",
		Type:      "node_operation",
		Status:    "queued",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveAgentCommand(ctx, cmd))

	leased, ok, err := store.AcquireAgentCommandLease(ctx, "host-a", "cmd-1", "owner-1", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, leased.Attempt)
	require.Equal(t, "owner-1", leased.LeaseOwner)

	_, ok, err = store.AcquireAgentCommandLease(ctx, "host-a", "cmd-1", "owner-2", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)

	leased.LeaseUntil = time.Now().Add(-time.Second)
	leased.Status = "queued"
	require.NoError(t, store.SaveAgentCommand(ctx, *leased))
	leased, ok, err = store.AcquireAgentCommandLease(ctx, "host-a", "cmd-1", "owner-2", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, leased.Attempt)
	require.Equal(t, "owner-2", leased.LeaseOwner)
}

func TestAgentCommandsRoutes(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	_, err := s.publishAgentCommand(context.Background(), "host-a", controlplane.AgentCommand{Type: "node_operation"})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents/host-a/commands", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"commands"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents/host-a/commands/list", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	server := httptest.NewServer(s)
	defer server.Close()
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = "/v1/agents/host-a/commands/stream"
	conn, _, err := websocket.Dial(context.Background(), u.String(), nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
}

func TestConsoleSessionPublishesAgentCommand(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "lab"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "lab", "field.sysbox.hcl"), []byte(`
resource "sysbox_image" "alpine" {
  substrate = "docker"
  docker_ref = "alpine:latest"
}

resource "sysbox_node" "web" {
  substrate = "docker"
  image = sysbox_image.alpine.id
}
`), 0o644))
	s := NewServer(runs, workspaces)
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	server := httptest.NewServer(s)
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):] + "/v1/agents/host-a/commands/stream"
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	cmdCh := make(chan controlplane.AgentCommand, 1)
	go func() {
		var cmd controlplane.AgentCommand
		_, data, err := conn.Read(context.Background())
		if err == nil {
			_ = json.Unmarshal(data, &cmd)
		}
		cmdCh <- cmd
	}()

	time.Sleep(50 * time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/nodes/web/sessions", bytes.NewBufferString(`{"shell":"/bin/sh","cols":80,"rows":24}`))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"agent_id":"host-a"`)

	select {
	case cmd := <-cmdCh:
		require.Equal(t, "session_open", cmd.Type)
		require.NotNil(t, cmd.Session)
		require.Equal(t, "web", cmd.Session.Node)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session command")
	}
}

func TestConsoleSessionRBACAndAudit(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "lab"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "lab", "field.sysbox.hcl"), []byte(`
resource "sysbox_image" "alpine" {
  substrate = "docker"
  docker_ref = "alpine:latest"
}

resource "sysbox_node" "web" {
  substrate = "docker"
  image = sysbox_image.alpine.id
}
`), 0o644))
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = runs
	cfg.Paths.WorkspacesDir = workspaces
	cfg.API.Console.AllowedRoles = []string{"console"}
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/nodes/web/sessions", bytes.NewBufferString(`{"shell":"/bin/sh"}`))
	req.Header.Set("X-Sysbox-User", "alice")
	req.Header.Set("X-Sysbox-Roles", "viewer")
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"denied"`)
	require.Contains(t, rec.Body.String(), `"actor":"alice"`)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/nodes/web/sessions", bytes.NewBufferString(`{"shell":"/bin/sh"}`))
	req.Header.Set("X-Sysbox-User", "bob")
	req.Header.Set("X-Sysbox-Roles", "console")
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"requested_by":"bob"`)
	require.Contains(t, rec.Body.String(), `"roles":["console"]`)
	require.Contains(t, rec.Body.String(), `"policy":"console.rbac"`)
	require.Contains(t, rec.Body.String(), `"action":"allow"`)
}

func TestRunDefaultsToLocalAgent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	run := s.jobs.start("mixed", "apply")
	require.Equal(t, DefaultAgentID, run.AgentID)

	stored, err := s.apiStore.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, DefaultAgentID, stored.AgentID)
}

func TestAgentStreamEndpointRemoved(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents/host-a/stream", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAgentRunCompletionUpdatesRunAndProjection(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	run := s.jobs.start("mixed", "apply")
	s.jobs.assign(run, "host-a")
	run.Status = RunDone
	run.AgentID = "host-a"

	body := bytes.NewBufferString(`{
  "run": {
    "id": "` + run.ID + `",
    "topology": "mixed",
    "op": "apply",
    "status": "done",
    "agent_id": "host-a"
  },
  "projection": {
    "agent_id": "host-a",
    "topology": "mixed",
    "workspace": "mixed",
    "backend": "local",
    "serial": 7,
    "resource_count": 3,
    "health": "healthy"
  }
}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/runs/"+run.ID+"/complete", body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, RunDone, got.Status)
	require.Equal(t, "host-a", got.AgentID)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents/host-a/projections", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"topology":"mixed"`)
	require.Contains(t, rec.Body.String(), `"resource_count":3`)
}

func TestAgentResourceProjectionUpdatesStatusProjection(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	body := bytes.NewBufferString(`{
  "agent_id": "host-a",
  "topology": "mixed",
  "workspace": "mixed",
  "health": {
    "status": "healthy",
    "healthy": 1,
    "resources": [
      {"resource":"sysbox_node.web","type":"sysbox_node","name":"web","provider":"docker","status":"healthy"}
    ]
  }
}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/projections/resources", body))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/mixed/resources", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	projections := s.agents.ListResourceProjections("mixed")
	require.Len(t, projections, 1)
	require.Equal(t, "sysbox_node.web", projections[0].Resources[0].Resource)
}

func TestAgentInventoryPersists(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	body := bytes.NewBufferString(`{
  "agent_id": "host-a",
  "capabilities": ["docker"],
  "labels": {"role":"lab"},
  "topologies": [{"workspace":"mixed","topology":"mixed","serial":3,"resource_count":2,"health":"healthy"}]
}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/inventory", body))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/agents/host-a/inventory", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"topology":"mixed"`)
	require.Contains(t, rec.Body.String(), `"resource_count":2`)
}

func TestNodeOperationCompletionPersists(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.saveAgent(context.Background(), controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}}))
	op := s.nodeOps.Create(controlplane.NodeOperation{
		Topology:    "mixed",
		Workspace:   "mixed",
		Operation:   "pause",
		Node:        "web",
		AgentID:     "host-a",
		RequestedBy: "alice",
		Roles:       []string{"operator"},
	})
	op.Status = "done"
	op.Audit = append(op.Audit, controlplane.Event{Action: "complete", Status: "done", Actor: "alice"})

	raw, err := json.Marshal(op)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/node-operations/"+op.ID+"/complete", bytes.NewReader(raw)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/node-operations/"+op.ID, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"done"`)
	require.Contains(t, rec.Body.String(), `"requested_by":"alice"`)
	require.Contains(t, rec.Body.String(), `"actor":"alice"`)
}
