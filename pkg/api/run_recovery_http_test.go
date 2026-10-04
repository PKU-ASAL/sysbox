package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func remoteRecoveryRequest(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("X-Sysbox-Roles", "admin")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}
func TestRemoteRecoveryAuthorizationUsesPinnedRevision(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", Status: "online", Capabilities: []string{controlplane.RunRecoveryCapability}}))
	files := map[string][]byte{"field.sysbox.hcl": []byte("# pinned configuration")}
	revision := controlplane.GlobalRevision{Revision: controlplane.ComputeRevisionDigest(files), Files: files}
	require.NoError(t, s.apiStore.SaveGlobalRevision(ctx, revision))
	run := controlplane.Run{ID: "failed", Topology: "lab", Operation: "destroy", Op: "destroy", AgentID: "owner", Status: controlplane.RunFailed, Recoverable: true, Revision: revision.Revision, QueuedAt: time.Now().Add(-time.Hour).UTC(), Attempt: 1, LeaseOwner: "old", LeaseUntil: time.Now().Add(-time.Minute)}
	run.Status = controlplane.RunRunning
	run.Recoverable = false
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	rec := remoteRecoveryRequest(t, s, "POST", "/v1/runs/failed/remote/recover", nil)
	require.Equal(t, 202, rec.Code, rec.Body.String())
	expired, err := s.apiStore.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunFailed, expired.Status)
	require.True(t, expired.Recoverable)
	var accepted map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
	lease, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "owner", accepted["command_id"], "dispatch", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	rec = remoteRecoveryRequest(t, s, "POST", "/v1/agents/owner/commands/"+lease.ID+"/recovery-authorize", controlplane.AgentCommandEvent{LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt})
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var auth controlplane.RunRecoveryAuthorization
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &auth))
	require.Equal(t, files, auth.Revision.Files)
	require.NoError(t, s.apiStore.SaveRun(ctx, controlplane.Run{ID: "other", Topology: "lab", Status: controlplane.RunAssigned, AgentID: "owner"}))
	rec = remoteRecoveryRequest(t, s, "POST", "/v1/agents/owner/commands/"+lease.ID+"/recovery-authorize", controlplane.AgentCommandEvent{LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt})
	require.Equal(t, 409, rec.Code)
}

func TestRemoteRunningObservationSurvivesControllerRestart(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	first := NewServer(root, workspace)
	ctx := context.Background()
	require.NoError(t, first.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", Status: "online", Capabilities: []string{controlplane.RunRecoveryCapability}}))
	run := controlplane.Run{ID: "running", Topology: "lab", Operation: "apply", AgentID: "owner", Status: controlplane.RunRunning, QueuedAt: time.Now().Add(-time.Minute).UTC(), Attempt: 1, LeaseOwner: "runner", LeaseUntil: time.Now().Add(time.Hour).UTC()}
	require.NoError(t, first.apiStore.SaveRun(ctx, run))
	denied := remoteRecoveryRequest(t, first, "POST", "/v1/runs/running/remote/recover", nil)
	require.Equal(t, 409, denied.Code)
	rec := remoteRecoveryRequest(t, first, "POST", "/v1/runs/running/remote/observe", nil)
	require.Equal(t, 202, rec.Code, rec.Body.String())
	var accepted map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
	restarted := NewServer(root, workspace)
	httpServer := httptest.NewServer(restarted)
	defer httpServer.Close()
	socketCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(socketCtx, "ws"+httpServer.URL[len("http"):]+"/v1/agents/owner/commands/stream", nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	_, raw, err := ws.Read(socketCtx)
	require.NoError(t, err)
	var cmd controlplane.AgentCommand
	require.NoError(t, json.Unmarshal(raw, &cmd))
	require.Equal(t, accepted["command_id"], cmd.ID)
	require.Equal(t, controlplane.RunRecoveryCommand, cmd.Type)
	proof := controlplane.AgentCommandEvent{CommandID: cmd.ID, AgentID: "owner", Type: cmd.Type, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt}
	authorized := remoteRecoveryRequest(t, restarted, "POST", "/v1/agents/owner/commands/"+cmd.ID+"/recovery-authorize", proof)
	require.Equal(t, 200, authorized.Code, authorized.Body.String())
	proof.Status = "completed"
	proof.Recovery = &controlplane.RunRecoveryReport{RunAttempt: run.Attempt, RunLeaseOwner: run.LeaseOwner, CommandID: cmd.ID, RunID: run.ID, AgentID: "owner", QueuedAt: run.QueuedAt, Mode: "observe", TopologyUID: "lineage", StateSerial: 4, ObservedAt: time.Now().UTC(), Resources: []controlplane.RecoveredResource{{ActionID: "action", Resource: "sysbox_node.web", OwnerUID: "lineage", ResourceUID: "web", Generation: 2, ExternalID: "vm", Status: "present", JournalStatus: "succeeded"}}}
	payload, err := json.Marshal(proof)
	require.NoError(t, err)
	require.NoError(t, ws.Write(socketCtx, websocket.MessageText, payload))
	require.Eventually(t, func() bool {
		_, err := restarted.apiStore.GetRunRecoveryReport(ctx, "owner", cmd.ID)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	again := NewServer(root, workspace)
	got := remoteRecoveryRequest(t, again, "GET", "/v1/runs/running/remote-reports/"+cmd.ID, nil)
	require.Equal(t, 200, got.Code, got.Body.String())
	require.Contains(t, got.Body.String(), "\"generation\":2")
	current, err := again.apiStore.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunRunning, current.Status)
}
func TestRecoveryAuthorizationRejectsDifferentExecutionAndCapability(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", Status: "online"}))
	run := controlplane.Run{ID: "run", Topology: "lab", Operation: "apply", AgentID: "owner", Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Attempt: 2, LeaseOwner: "fresh", LeaseUntil: time.Now().Add(time.Hour)}
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	rec := remoteRecoveryRequest(t, s, "POST", "/v1/runs/run/remote/observe", nil)
	require.Equal(t, 409, rec.Code)
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", Status: "online", Capabilities: []string{controlplane.RunRecoveryCapability}}))
	stale := run
	stale.Attempt = 1
	stale.LeaseOwner = "old"
	cmd := controlplane.AgentCommand{ID: "stale", AgentID: "owner", Type: controlplane.RunRecoveryCommand, RecoveryMode: "observe", Run: &stale}
	require.NoError(t, s.apiStore.SaveAgentCommand(ctx, cmd))
	lease, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "owner", cmd.ID, "dispatcher", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	rec = remoteRecoveryRequest(t, s, "POST", "/v1/agents/owner/commands/stale/recovery-authorize", controlplane.AgentCommandEvent{LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt})
	require.Equal(t, 409, rec.Code)
}
