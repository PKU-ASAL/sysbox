package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/stretchr/testify/require"
)

func TestAgentReregistrationPreservesControlPolicy(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	created := time.Now().Add(-time.Hour).UTC()
	require.NoError(t, s.agentService().Save(ctx, controlplane.Agent{ID: "agent", Name: "named", Status: controlplane.AgentStatusQuarantined, Quarantined: true, Reason: "maintenance", CreatedAt: created}))
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/agents", strings.NewReader(`{"id":"agent","version":"restarted"}`)))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	got, err := s.apiStore.GetAgent(ctx, "agent")
	require.NoError(t, err)
	require.True(t, got.Quarantined)
	require.Equal(t, controlplane.AgentStatusQuarantined, got.Status)
	require.Equal(t, "maintenance", got.Reason)
	require.Equal(t, "named", got.Name)
	require.Equal(t, "restarted", got.Version)
	require.True(t, created.Equal(got.CreatedAt))
}

func TestGuestRoutingDoesNotReportAgentStoreFailureAsMissingTarget(t *testing.T) {
	s := guestExecutionTestServer(t)
	s.agentService().store = failingAgentReads{Store: s.apiStore, failure: errors.New("agent storage unavailable")}
	for _, request := range []struct{ method, path, body string }{{http.MethodPost, "/v1/topologies/lab/nodes/web/executions", `{"argv":["true"]}`}, {http.MethodPut, "/v1/topologies/lab/nodes/web/files?path=/tmp/test&mode=0600", "content"}} {
		req := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
		req.Header.Set("X-Sysbox-Roles", "admin")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	}
}

func TestAgentReregistrationRequiresActualCredentialForProtectedIdentity(t *testing.T) {
	for _, body := range []string{`{"id":"victim","capabilities":["docker"]}`, `{"id":"victim","secret_hash":"known-hash","capabilities":["docker"]}`} {
		t.Run(body, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			ctx := context.Background()
			heartbeat := time.Now().Add(-time.Hour).UTC()
			require.NoError(t, s.agentService().Save(ctx, controlplane.Agent{ID: "victim", Status: controlplane.AgentStatusOffline, AuthSecret: "real-secret", SecretHash: "known-hash", Version: "original", LastHeartbeat: heartbeat}))
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/agents", strings.NewReader(body)))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			got, err := s.apiStore.GetAgent(ctx, "victim")
			require.NoError(t, err)
			require.Equal(t, controlplane.AgentStatusOffline, got.Status)
			require.True(t, heartbeat.Equal(got.LastHeartbeat))
			require.Empty(t, got.Capabilities)
		})
	}
}

func TestAgentRepositoryUpdatesCompleteRecord(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		ctx := context.Background()
		agent := controlplane.Agent{ID: "agent", Name: "old", Status: controlplane.AgentStatusOnline, AuthSecret: "old-secret", SecretHash: "old-hash"}
		require.NoError(t, s.SaveAgent(ctx, agent))
		agent.Name = "new"
		agent.AuthSecret = "new-secret"
		agent.SecretHash = "new-hash"
		require.NoError(t, s.SaveAgent(ctx, agent))
		got, err := s.GetAgent(ctx, agent.ID)
		require.NoError(t, err)
		require.Equal(t, agent.Name, got.Name)
		require.Equal(t, agent.AuthSecret, got.AuthSecret)
		require.Equal(t, agent.SecretHash, got.SecretHash)
	})
}

type failingAgentReads struct {
	controlstore.Store
	failure error
}

func (s failingAgentReads) GetAgent(context.Context, string) (*controlplane.Agent, error) {
	return nil, s.failure
}
func (s failingAgentReads) ListAgents(context.Context) ([]controlplane.Agent, error) {
	return nil, s.failure
}
func TestAgentRepositoryFailureCannotAuthorizeCachedAgent(t *testing.T) {
	failure := errors.New("storage unavailable")
	registry := newAgentRegistry()
	registry.Save(controlplane.Agent{ID: "cached", Status: controlplane.AgentStatusOnline})
	service := &AgentService{store: failingAgentReads{failure: failure}, registry: registry}
	_, err := service.Get(context.Background(), "cached")
	require.ErrorIs(t, err, failure)
	scheduler := SchedulerService{agents: service}
	_, err = scheduler.SelectAgent(context.Background(), nil, "")
	require.ErrorIs(t, err, failure)
}

type staleAgentInventory struct {
	controlstore.Store
	snapshot controlplane.Agent
}

func (s staleAgentInventory) ListAgents(context.Context) ([]controlplane.Agent, error) {
	return []controlplane.Agent{s.snapshot}, nil
}
func TestAgentOfflineObservationCannotOverwriteFreshHeartbeat(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		now := time.Now().UTC()
		fresh := controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline, LastHeartbeat: now, Version: "fresh"}
		require.NoError(t, s.SaveAgent(context.Background(), fresh))
		stale := fresh
		stale.LastHeartbeat = now.Add(-time.Hour)
		stale.Version = "stale"
		service := AgentService{store: staleAgentInventory{Store: s, snapshot: stale}, agentOfflineTTL: func() time.Duration { return time.Minute }}
		service.MarkStaleOffline(context.Background(), now)
		got, err := s.GetAgent(context.Background(), fresh.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.AgentStatusOnline, got.Status)
		require.Equal(t, "fresh", got.Version)
		require.True(t, now.Equal(got.LastHeartbeat))
	})
}
func TestAgentHeartbeatCannotRewriteControlPolicyOrCredentials(t *testing.T) {
	server := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	require.NoError(t, server.agentService().Save(ctx, controlplane.Agent{ID: "agent", Name: "named", Status: controlplane.AgentStatusOnline, AuthSecret: "original"}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/agents/agent/heartbeat", strings.NewReader(`{"disabled":true,"quarantined":true,"reason":"spoofed","auth_secret":"replacement","secret_hash":"replacement","version":"fresh"}`))
	server.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	got, err := server.apiStore.GetAgent(ctx, "agent")
	require.NoError(t, err)
	require.False(t, got.Disabled)
	require.False(t, got.Quarantined)
	require.Equal(t, "original", got.AuthSecret)
	require.Empty(t, got.SecretHash)
	require.Equal(t, "named", got.Name)
	require.Equal(t, "fresh", got.Version)
}
