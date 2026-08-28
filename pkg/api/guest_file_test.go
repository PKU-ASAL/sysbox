package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestGuestFilePutValidatesPathModeDigestAndStagesPrivately(t *testing.T) {
	s := guestExecutionTestServer(t)
	s.cfg.API.RBAC.AdminRoles = []string{"admin"}
	req := httptest.NewRequest(http.MethodPut, "/v1/topologies/lab/nodes/web/files?path=/opt/challenge/flag&mode=0400", bytes.NewReader([]byte{0, 1, 2}))
	req.Header.Set("X-Sysbox-Roles", "admin")
	req.Header.Set("X-Content-SHA256", "ae4b3280e56e2faf83f414a6e3dabe9d5fbe18976544c05fed121accb85b53fc")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), s.runsDir)
	require.NotContains(t, rec.Body.String(), "fetch_ref")
	require.NotContains(t, rec.Body.String(), "/opt/challenge/flag")

	for _, path := range []string{"relative", "/tmp/../etc/passwd", "/tmp/%00bad"} {
		req = httptest.NewRequest(http.MethodPut, "/v1/topologies/lab/nodes/web/files?path="+path+"&mode=0400", bytes.NewReader([]byte("secret")))
		req.Header.Set("X-Sysbox-Roles", "admin")
		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.NotContains(t, rec.Body.String(), "secret")
	}
}

func TestGuestFileOperationHTTPDurableLifecycle(t *testing.T) {
	s := guestExecutionTestServer(t)
	s.cfg.API.RBAC.AdminRoles = []string{"admin"}

	put := httptest.NewRequest(http.MethodPut, "/v1/topologies/lab/nodes/web/files?path=/opt/private/flag&mode=0400", bytes.NewReader([]byte("secret")))
	put.Header.Set("X-Sysbox-Roles", "admin")
	putRec := httptest.NewRecorder()
	s.ServeHTTP(putRec, put)
	require.Equal(t, http.StatusAccepted, putRec.Code, putRec.Body.String())
	var created struct{ ID, Status string }
	require.NoError(t, json.Unmarshal(putRec.Body.Bytes(), &created))
	require.Equal(t, controlplane.GuestExecutionQueued, created.Status)

	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/file-operations/"+created.ID, nil)
		req.Header.Set("X-Sysbox-Roles", "admin")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	rec := get()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"queued"`)
	for _, private := range []string{"host-a", "/opt/private/flag", "fetch_ref", "agent_id", "lease_owner", "secret"} {
		require.NotContains(t, rec.Body.String(), private)
	}

	start := httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/guest-file-operations/"+created.ID+"/start", nil)
	startRec := httptest.NewRecorder()
	s.ServeHTTP(startRec, start)
	require.Equal(t, http.StatusOK, startRec.Code, startRec.Body.String())
	rec = get()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"running"`)

	complete := httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/guest-file-operations/"+created.ID+"/complete", bytes.NewBufferString(`{}`))
	completeRec := httptest.NewRecorder()
	s.ServeHTTP(completeRec, complete)
	require.Equal(t, http.StatusOK, completeRec.Code, completeRec.Body.String())
	rec = get()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"completed"`)
}

func TestGuestFileOperationHTTPCancel(t *testing.T) {
	s := guestExecutionTestServer(t)
	s.cfg.API.RBAC.AdminRoles = []string{"admin"}
	put := httptest.NewRequest(http.MethodPut, "/v1/topologies/lab/nodes/web/files?path=/flag&mode=0400", bytes.NewReader([]byte("secret")))
	put.Header.Set("X-Sysbox-Roles", "admin")
	putRec := httptest.NewRecorder()
	s.ServeHTTP(putRec, put)
	var created struct{ ID string }
	require.NoError(t, json.Unmarshal(putRec.Body.Bytes(), &created))

	cancel := httptest.NewRequest(http.MethodPost, "/v1/file-operations/"+created.ID+"/cancel", nil)
	cancel.Header.Set("X-Sysbox-Roles", "admin")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, cancel)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"cancelled"`)
	op, err := s.apiStore.GetGuestFileOperation(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionCancelled, op.Status)
	cmd, err := s.agentService().FindCommand(context.Background(), "host-a", op.CommandID)
	require.NoError(t, err)
	_, ok := s.agentService().AcquireCommandLease(context.Background(), "host-a", cmd)
	require.False(t, ok)
}

func TestGuestFileOperationSurvivesLocalServerRestart(t *testing.T) {
	runsDir := t.TempDir()
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = runsDir
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.API.RBAC.AdminRoles = []string{"admin"}
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.apiStore.SaveGuestFileOperation(context.Background(), controlplane.GuestFileOperation{ID: "file-op-1", Version: 1, AgentID: "host-a", CommandID: "private-command", Topology: "lab", Node: "web", Status: controlplane.GuestExecutionQueued}))

	restarted := NewServerWithConfig(cfg)
	req := httptest.NewRequest(http.MethodGet, "/v1/file-operations/file-op-1", nil)
	req.Header.Set("X-Sysbox-Roles", "admin")
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"queued"`)
	require.NotContains(t, rec.Body.String(), "/private")
}

func TestGuestFileOperationReportsAreIdempotentAndOrdered(t *testing.T) {
	s := guestExecutionTestServer(t)
	op := controlplane.GuestFileOperation{ID: "op-1", Version: 1, AgentID: "host-a", CommandID: "cmd-1", Status: controlplane.GuestExecutionQueued}
	require.NoError(t, s.apiStore.SaveGuestFileOperation(context.Background(), op))
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)))
		return rec
	}
	completePath := "/v1/agents/host-a/guest-file-operations/op-1/complete"
	startPath := "/v1/agents/host-a/guest-file-operations/op-1/start"
	require.Equal(t, http.StatusConflict, post(completePath, `{}`).Code)
	require.Equal(t, http.StatusOK, post(startPath, ``).Code)
	require.Equal(t, http.StatusOK, post(startPath, ``).Code)
	require.Equal(t, http.StatusOK, post(completePath, `{}`).Code)
	require.Equal(t, http.StatusOK, post(completePath, `{}`).Code)
}

func TestCancelledGuestFileOperationRejectsLateCompletion(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	svc := newGuestFileOperationService(store)
	require.NoError(t, store.SaveGuestFileOperation(context.Background(), controlplane.GuestFileOperation{ID: "op", Version: 1, Status: controlplane.GuestExecutionQueued}))
	_, err := svc.Cancel(context.Background(), "op")
	require.NoError(t, err)
	_, err = svc.Complete(context.Background(), "op", "")
	require.Error(t, err)
	got, err := svc.Get(context.Background(), "op")
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionCancelled, got.Status)
}

func TestRunningGuestFileOperationCancelDispatchesCancelCommand(t *testing.T) {
	s := guestExecutionTestServer(t)
	s.cfg.API.RBAC.AdminRoles = []string{"admin"}
	put := httptest.NewRequest(http.MethodPut, "/v1/topologies/lab/nodes/web/files?path=/flag&mode=0400", bytes.NewReader([]byte("secret")))
	put.Header.Set("X-Sysbox-Roles", "admin")
	putRec := httptest.NewRecorder()
	s.ServeHTTP(putRec, put)
	var created controlplane.GuestFileOperation
	require.NoError(t, json.Unmarshal(putRec.Body.Bytes(), &created))
	startRec := httptest.NewRecorder()
	s.ServeHTTP(startRec, httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/guest-file-operations/"+created.ID+"/start", nil))
	require.Equal(t, http.StatusOK, startRec.Code)
	cancel := httptest.NewRequest(http.MethodPost, "/v1/file-operations/"+created.ID+"/cancel", nil)
	cancel.Header.Set("X-Sysbox-Roles", "admin")
	cancelRec := httptest.NewRecorder()
	s.ServeHTTP(cancelRec, cancel)
	require.Equal(t, http.StatusAccepted, cancelRec.Code, cancelRec.Body.String())
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	found := false
	for _, cmd := range commands {
		found = found || (cmd.Type == "cancel_command" && cmd.Operation.ExternalID == created.ID)
	}
	require.True(t, found)
}

func TestCancelIntentBlocksConcurrentStartAndCompletion(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	svc := newGuestFileOperationService(store)
	require.NoError(t, store.SaveGuestFileOperation(context.Background(), controlplane.GuestFileOperation{ID: "op", Version: 1, Status: controlplane.GuestExecutionQueued}))
	op, err := svc.RequestCancel(context.Background(), "op")
	require.NoError(t, err)
	require.True(t, op.CancelRequested)
	_, err = svc.MarkRunning(context.Background(), "op")
	require.ErrorIs(t, err, errGuestFileOperationConflict)
	_, err = svc.Complete(context.Background(), "op", "")
	require.ErrorIs(t, err, errGuestFileOperationConflict)
	op, err = svc.Cancel(context.Background(), "op")
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionCancelled, op.Status)
}

func TestGuestFileFailedCommandEventReconcilesDurableOperation(t *testing.T) {
	s := guestExecutionTestServer(t)

	// A completed command leaves the durable operation running.
	done := controlplane.GuestFileOperation{ID: "op-done", Version: 1, AgentID: "host-a", CommandID: "cmd-done", Status: controlplane.GuestExecutionRunning}
	require.NoError(t, s.apiStore.SaveGuestFileOperation(context.Background(), done))
	_, err := s.agentService().PublishCommand(context.Background(), "host-a", controlplane.AgentCommand{ID: "cmd-done", Type: "guest_file_put", FilePut: &controlplane.GuestFilePut{ID: "op-done"}})
	require.NoError(t, err)
	s.agentService().RecordCommandEvent(context.Background(), controlplane.AgentCommandEvent{CommandID: "cmd-done", AgentID: "host-a", Status: controlplane.AgentCommandStatusCompleted})
	got, err := s.apiStore.GetGuestFileOperation(context.Background(), "op-done")
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionRunning, got.Status)

	// A failed command reconciles the durable operation to failed.
	failed := controlplane.GuestFileOperation{ID: "op-failed", Version: 1, AgentID: "host-a", CommandID: "cmd-failed", Status: controlplane.GuestExecutionRunning}
	require.NoError(t, s.apiStore.SaveGuestFileOperation(context.Background(), failed))
	_, err = s.agentService().PublishCommand(context.Background(), "host-a", controlplane.AgentCommand{ID: "cmd-failed", Type: "guest_file_put", FilePut: &controlplane.GuestFilePut{ID: "op-failed"}})
	require.NoError(t, err)
	s.agentService().RecordCommandEvent(context.Background(), controlplane.AgentCommandEvent{CommandID: "cmd-failed", AgentID: "host-a", Status: controlplane.AgentCommandStatusFailed})
	got, err = s.apiStore.GetGuestFileOperation(context.Background(), "op-failed")
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionFailed, got.Status)
}
