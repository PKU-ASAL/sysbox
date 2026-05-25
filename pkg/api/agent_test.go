package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

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
}

func TestAgentCommandWebSocketReceivesAssignedRunAndReportsAck(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.agents.Save(controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}})
	server := httptest.NewServer(s)
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):] + "/v1/agents/host-a/commands"
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
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for command websocket")
	}
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
	s.agents.Save(controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}})
	server := httptest.NewServer(s)
	defer server.Close()

	errCh := make(chan error, 1)
	bodyCh := make(chan string, 1)
	go func() {
		resp, err := http.Get(server.URL + "/v1/agents/host-a/stream")
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		data := make([]byte, 4096)
		n, err := resp.Body.Read(data)
		if err != nil && err != io.EOF {
			errCh <- err
			return
		}
		bodyCh <- string(data[:n])
	}()

	time.Sleep(50 * time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/nodes/web/sessions", bytes.NewBufferString(`{"shell":"/bin/sh","cols":80,"rows":24}`))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"agent_id":"host-a"`)

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case body := <-bodyCh:
		require.Contains(t, body, "session_open")
		require.Contains(t, body, "web")
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
	s.agents.Save(controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}})

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

func TestAgentStreamReceivesAssignedRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.agents.Save(controlplane.Agent{ID: "host-a", Status: "online", Capabilities: []string{"docker"}})
	server := httptest.NewServer(s)
	defer server.Close()

	errCh := make(chan error, 1)
	bodyCh := make(chan string, 1)
	go func() {
		resp, err := http.Get(server.URL + "/v1/agents/host-a/stream")
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		data := make([]byte, 4096)
		n, err := resp.Body.Read(data)
		if err != nil && err != io.EOF {
			errCh <- err
			return
		}
		bodyCh <- string(data[:n])
	}()

	time.Sleep(50 * time.Millisecond)
	run := s.jobs.start("mixed", "apply")
	require.NoError(t, s.dispatchRun(context.Background(), run, []string{"docker"}))

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case body := <-bodyCh:
		require.Contains(t, body, "data:")
		require.Contains(t, body, "run_assigned")
		require.Contains(t, body, run.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent stream command")
	}
}

func TestAgentRunCompletionUpdatesRunAndProjection(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
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

func TestNodeOperationCompletionPersists(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
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
