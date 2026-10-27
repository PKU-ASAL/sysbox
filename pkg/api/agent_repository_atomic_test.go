package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestAgentRepositoryReopensWithoutRegistryAuthority(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		created := time.Now().Add(-time.Hour).UTC()
		require.NoError(t, s.SaveAgent(ctx, controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline, CreatedAt: created, AuthSecret: "original", Name: "stable"}))
		yes := true
		_, err := s.UpdateAgentPolicy(ctx, "agent", persistence.AgentPolicy{Disabled: &yes, Reason: "admin"})
		require.NoError(t, err)
		var reopened apiStore
		switch store := s.(type) {
		case *localAPIStore:
			reopened = &localAPIStore{runsDir: store.runsDir}
		case *sqliteAPIStore:
			next := &sqliteAPIStore{dbPath: store.dbPath, runsDir: store.runsDir}
			db, err := next.open()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			reopened = next
		default:
			reopened = s
		}
		svc := &AgentService{store: reopened, registry: newAgentRegistry()}
		got, err := svc.Heartbeat(ctx, controlplane.Agent{ID: "agent", Version: "after-restart"})
		require.NoError(t, err)
		require.True(t, got.Disabled)
		require.Equal(t, "admin", got.Reason)
		require.Equal(t, "original", got.AuthSecret)
		require.True(t, created.Equal(got.CreatedAt))
	})
}
func TestAgentEmptyRepositoryDoesNotReviveCachedAgent(t *testing.T) {
	svc := &AgentService{store: &localAPIStore{runsDir: t.TempDir()}, registry: newAgentRegistry()}
	svc.registry.Save(controlplane.Agent{ID: "obsolete", Status: controlplane.AgentStatusOnline})
	got, err := svc.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, got)
	_, err = svc.Get(context.Background(), "obsolete")
	require.ErrorIs(t, err, persistence.ErrNotFound)
}
func TestAgentReregistrationRejectsCredentialReplacement(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		original := controlplane.Agent{ID: "agent", AuthSecret: "known", SecretHash: "known-hash"}
		_, err := s.RegisterAgent(ctx, original)
		require.NoError(t, err)
		_, err = s.RegisterAgent(ctx, original)
		require.NoError(t, err)
		changed := original
		changed.AuthSecret = "replacement"
		_, err = s.RegisterAgent(ctx, changed)
		require.ErrorIs(t, err, persistence.ErrConflict)
		got, err := s.GetAgent(ctx, "agent")
		require.NoError(t, err)
		require.Equal(t, original.AuthSecret, got.AuthSecret)
	})
}
func TestSQLiteAgentCommitFailureDoesNotAdvanceRegistry(t *testing.T) {
	ctx := context.Background()
	s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "agents.db")}
	svc := &AgentService{store: s, registry: newAgentRegistry()}
	require.NoError(t, svc.Save(ctx, controlplane.Agent{ID: "agent", Version: "before", Status: controlplane.AgentStatusOnline}))
	db, err := s.open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, "CREATE TRIGGER reject_agent_update BEFORE UPDATE ON sysbox_agents BEGIN SELECT RAISE(ABORT, 'injected write failure'); END")
	require.NoError(t, err)
	got, err := svc.Heartbeat(ctx, controlplane.Agent{ID: "agent", Version: "after"})
	require.Error(t, err)
	require.Nil(t, got)
	cached, err := svc.registry.Get("agent")
	require.NoError(t, err)
	require.Equal(t, "before", cached.Version)
	stored, err := s.GetAgent(ctx, "agent")
	require.NoError(t, err)
	require.Equal(t, "before", stored.Version)
	require.True(t, stored.LastHeartbeat.IsZero())
}

func TestAgentRepositoryConcurrentPolicyAndHeartbeatPreserveBothFlags(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		require.NoError(t, s.SaveAgent(ctx, controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline, Name: "stable", AuthSecret: "original"}))
		start := make(chan struct{})
		results := make(chan error, 18)
		for i := 0; i < 18; i++ {
			go func(i int) {
				<-start
				yes := true
				var err error
				switch i % 3 {
				case 0:
					_, err = s.UpdateAgentPolicy(ctx, "agent", persistence.AgentPolicy{Disabled: &yes})
				case 1:
					_, err = s.UpdateAgentPolicy(ctx, "agent", persistence.AgentPolicy{Quarantined: &yes})
				default:
					_, err = s.HeartbeatAgent(ctx, controlplane.Agent{ID: "agent", Version: "fresh", AuthSecret: "spoofed"})
				}
				results <- err
			}(i)
		}
		close(start)
		for i := 0; i < 18; i++ {
			require.NoError(t, <-results)
		}
		got, err := s.GetAgent(ctx, "agent")
		require.NoError(t, err)
		require.True(t, got.Disabled)
		require.True(t, got.Quarantined)
		require.Equal(t, controlplane.AgentStatusDisabled, got.Status)
		require.Equal(t, "original", got.AuthSecret)
		require.Equal(t, "fresh", got.Version)
		require.False(t, got.LastHeartbeat.IsZero())
	})
}
func TestAgentRepositoryKeepsPolicyDuringOfflineExpiry(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		now := time.Now().UTC()
		old := controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline, LastHeartbeat: now.Add(-time.Hour)}
		require.NoError(t, s.SaveAgent(ctx, old))
		yes := true
		_, err := s.UpdateAgentPolicy(ctx, "agent", persistence.AgentPolicy{Quarantined: &yes, Reason: "maintenance"})
		require.NoError(t, err)
		got, err := s.ExpireAgentHeartbeat(ctx, "agent", now, time.Minute)
		require.NoError(t, err)
		require.True(t, got.Quarantined)
		require.Equal(t, "quarantined", got.Status)
		require.Equal(t, "maintenance", got.Reason)
		got, err = s.HeartbeatAgent(ctx, controlplane.Agent{ID: "agent", Version: "reported"})
		require.NoError(t, err)
		require.True(t, got.Quarantined)
		require.Equal(t, "maintenance", got.Reason)
	})
}
func TestAgentRepositoryRejectsInvalidAndMissingIdentities(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		_, err := s.GetAgent(ctx, "missing")
		require.ErrorIs(t, err, persistence.ErrNotFound)
		_, err = s.HeartbeatAgent(ctx, controlplane.Agent{ID: "missing"})
		require.ErrorIs(t, err, persistence.ErrNotFound)
		require.ErrorIs(t, s.SaveAgent(ctx, controlplane.Agent{ID: "../escape"}), persistence.ErrInvalidRecord)
		require.ErrorIs(t, s.SaveAgent(ctx, controlplane.Agent{ID: "bad-status", Status: "unrecognized"}), persistence.ErrInvalidRecord)
		_, err = s.UpdateAgentPolicy(ctx, "../escape", persistence.AgentPolicy{})
		require.ErrorIs(t, err, persistence.ErrInvalidRecord)
	})
}
func TestAgentRepositoryCorruptionCannotBeReplacedOrCached(t *testing.T) {
	for _, raw := range []string{"null", "{}", `{"id":"agent","protocol":"agent.v1"}`, `{"id":"agent","status":"online"}`, `{"id":"different","status":"online","protocol":"agent.v1"}`, `{"id":"agent","status":"online","protocol":"agent.v1","last_heartbeat":"private-value"}`} {
		t.Run(raw, func(t *testing.T) {
			s := &localAPIStore{runsDir: t.TempDir()}
			path := filepath.Join(s.runsDir, "_agents", "agent", "agent.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
			_, err := s.GetAgent(context.Background(), "agent")
			require.ErrorIs(t, err, persistence.ErrCorruptRecord)
			require.NotContains(t, err.Error(), "private-value")
			require.ErrorIs(t, s.SaveAgent(context.Background(), controlplane.Agent{ID: "agent"}), persistence.ErrCorruptRecord)
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, raw, string(got))
		})
	}
}
func TestSQLiteAgentRepositoryRejectsMalformedMetadata(t *testing.T) {
	for _, mutation := range []string{"capabilities='not-json'", "labels='[]'", "last_heartbeat='private-value'", "disabled=2", "protocol='unsupported'", "protocol=''", "status=''"} {
		t.Run(mutation, func(t *testing.T) {
			s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "agents.db")}
			ctx := context.Background()
			require.NoError(t, s.SaveAgent(ctx, controlplane.Agent{ID: "agent"}))
			db, err := s.open()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			_, err = db.ExecContext(ctx, "UPDATE sysbox_agents SET "+mutation+" WHERE id='agent'")
			require.NoError(t, err)
			_, err = s.GetAgent(ctx, "agent")
			require.ErrorIs(t, err, persistence.ErrCorruptRecord)
			require.NotContains(t, err.Error(), "private-value")
			_, err = s.HeartbeatAgent(ctx, controlplane.Agent{ID: "agent"})
			require.ErrorIs(t, err, persistence.ErrCorruptRecord)
		})
	}
}
func TestAgentHTTPReportsStorageFailuresWithoutCachedFallback(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.agents.Save(controlplane.Agent{ID: "cached", Status: controlplane.AgentStatusOnline})
	s.apiStore = failingAgentReads{apiStore: s.apiStore, failure: errors.New("storage unavailable")}
	s.agentService().store = s.apiStore
	for _, path := range []string{"/v1/agents", "/v1/agents/cached"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		var response map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		require.IsType(t, "", response["error"])
	}
}
