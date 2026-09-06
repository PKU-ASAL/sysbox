package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestClaimAgentRunRequiresSignatureWhenSecretRegistered(t *testing.T) {
	const secret = "agent-secret"
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID:           "host-a",
		Status:       "online",
		AuthSecret:   secret,
		SecretHash:   agent.SecretHash(secret),
		Capabilities: []string{"docker"},
	}))

	run := s.jobs.startWithOptions("mixed", "apply", runStartOptions{AgentID: "host-a"})
	s.jobs.assign(run, "host-a")

	// An unauthenticated claim for an agent that has a secret must be rejected.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/runs/"+run.ID+"/claim", nil)
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

	// A correctly signed claim succeeds.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/runs/"+run.ID+"/claim", nil)
	require.NoError(t, agent.SignRequest(req, "host-a", secret, time.Now()))
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"id":"`+run.ID+`"`)
}

func TestClaimAgentRunAllowsSecretlessAgent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID:           "host-b",
		Status:       "online",
		Capabilities: []string{"docker"},
	}))

	run := s.jobs.startWithOptions("mixed", "apply", runStartOptions{AgentID: "host-b"})
	s.jobs.assign(run, "host-b")

	// A secret-less agent has nothing to verify against, so claim still works.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/host-b/runs/"+run.ID+"/claim", nil)
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"id":"`+run.ID+`"`)
}
