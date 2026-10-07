package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGuestCompletionHTTPRejectsMissingProof(t *testing.T) {
	s := guestExecutionTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/topologies/lab/nodes/web/executions", bytes.NewBufferString(`{"argv":["true"]}`))
	req.Header.Set("X-Sysbox-Roles", "admin")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)
	var created controlplane.GuestExecution
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	rec = httptest.NewRecorder()
	completion := controlplane.GuestExecutionCompletion{Result: controlplane.GuestExecutionResult{ExitCode: 0}}
	raw, _ := json.Marshal(completion)
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/guest-executions/"+created.ID+"/complete", bytes.NewReader(raw))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	// A valid proof from a leased command succeeds.
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	cmd := commands[0]
	require.NotEmpty(t, cmd.ActionID)
	require.NotEmpty(t, cmd.ResourceUID)
	require.Greater(t, cmd.Generation, int64(0))
	require.Greater(t, cmd.LeaseEpoch, int64(0))
	require.Equal(t, created.ID, cmd.Execution.ID)
	lease, ok, err := s.apiStore.AcquireAgentCommandLease(context.Background(), "host-a", cmd.ID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, lease.LeaseUntil.After(time.Now()))
	completion.ActionID, completion.ResourceUID, completion.Generation, completion.LeaseEpoch, completion.LeaseOwner, completion.Attempt = lease.ActionID, lease.ResourceUID, lease.Generation, lease.LeaseEpoch, lease.LeaseOwner, lease.Attempt
	raw, _ = json.Marshal(completion)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/guest-executions/"+created.ID+"/complete", bytes.NewReader(raw))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
