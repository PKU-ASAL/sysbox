package api

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDurableClaimReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			cfg := config.MustLoadServiceConfig("")
			cfg.Paths.RunsDir = t.TempDir()
			cfg.Paths.WorkspacesDir = t.TempDir()
			if backend == "postgres" {
				cfg.State.Backend = isolatedPostgresTestDSN(t)
			} else {
				cfg.State.Backend = "sqlite://" + t.TempDir() + "/state.db"
			}
			s := NewServerWithConfig(cfg)
			ctx := context.Background()
			original := controlplane.Run{ID: "claim-run", Topology: "claim-topology", AgentID: "agent", Status: controlplane.RunAssigned, DeadlineAt: time.Now().UTC().Add(time.Hour)}
			require.NoError(t, s.apiStore.SaveRun(ctx, original))
			first, ok, err := s.apiStore.ClaimRun(ctx, original.ID, "agent", "claim-one", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			// A lost response and API restart must preserve the original lease exactly.
			restarted := NewServerWithConfig(cfg)
			replay, ok, err := restarted.apiStore.ClaimRun(ctx, original.ID, "agent", "claim-one", time.Hour)
			require.NoError(t, err)
			require.True(t, ok, "same claimant should recover committed receipt")
			require.Equal(t, first, replay)
			for _, identity := range [][2]string{{"other", "claim-one"}, {"agent", "claim-two"}} {
				_, ok, err = restarted.apiStore.ClaimRun(ctx, original.ID, identity[0], identity[1], time.Minute)
				require.NoError(t, err)
				require.False(t, ok)
			}
			first.LeaseUntil = time.Now().Add(-time.Second)
			require.NoError(t, s.apiStore.SaveRun(ctx, *first))
			_, ok, err = s.apiStore.ClaimRun(ctx, original.ID, "agent", "claim-one", time.Minute)
			require.NoError(t, err)
			require.False(t, ok)
			first.Status = controlplane.RunDone
			first.LeaseUntil = time.Now().Add(time.Minute)
			require.NoError(t, s.apiStore.SaveRun(ctx, *first))
			_, ok, err = s.apiStore.ClaimRun(ctx, original.ID, "agent", "claim-one", time.Minute)
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}

func TestClaimHTTPReplayAndDifferentExecutionFencing(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = "sqlite://" + t.TempDir() + "/state.db"
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "agent", Status: "online"}))
	require.NoError(t, s.apiStore.SaveRun(context.Background(), controlplane.Run{ID: "run", Topology: "topo", AgentID: "agent", Status: controlplane.RunAssigned}))
	claim := func(id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/agents/agent/runs/run/claim", strings.NewReader(`{"claim_id":"`+id+`"}`))
		s.ServeHTTP(rec, req)
		return rec
	}
	first := claim("8e552a37-152b-4567-bb41-196fa8c81a27")
	require.Equal(t, 200, first.Code, first.Body.String())
	second := claim("8e552a37-152b-4567-bb41-196fa8c81a27")
	require.Equal(t, 200, second.Code, second.Body.String())
	require.JSONEq(t, first.Body.String(), second.Body.String())
	require.Equal(t, 409, claim("8e552a37-152b-4567-bb41-196fa8c81a28").Code)
}

func TestClaimReplayAfterAPIInputRehydration(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = "sqlite://" + t.TempDir() + "/state.db"
	s := NewServerWithConfig(cfg)
	registerDockerAgent(t, s)
	rev := publishRevision(t, s, upsertApplyHCL)
	w := keyedApply(t, s, rev, "claim-hydration", "private-claim-canary")
	require.Equal(t, 202, w.Code, w.Body.String())
	var receipt struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &receipt))
	first, err := s.jobs.claim(receipt.RunID, "host-a", "execution-one")
	require.NoError(t, err)
	restart := NewServerWithConfig(cfg)
	_, err = restart.jobs.claim(receipt.RunID, "host-a", "execution-one")
	require.ErrorIs(t, err, errApplyInputsRequired)
	require.Equal(t, 202, keyedApply(t, restart, rev, "claim-hydration", "private-claim-canary").Code)
	replay, err := restart.jobs.claim(receipt.RunID, "host-a", "execution-one")
	require.NoError(t, err)
	require.Equal(t, first, replay)
	durable, err := restart.apiStore.GetRun(context.Background(), receipt.RunID)
	require.NoError(t, err)
	raw, err := json.Marshal(durable)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-claim-canary")
}

func TestConcurrentClaimReplayKeepsOneLease(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			cfg := config.MustLoadServiceConfig("")
			cfg.Paths.RunsDir = t.TempDir()
			cfg.Paths.WorkspacesDir = t.TempDir()
			if backend == "postgres" {
				cfg.State.Backend = isolatedPostgresTestDSN(t)
			} else {
				cfg.State.Backend = "sqlite://" + t.TempDir() + "/state.db"
			}
			servers := []*Server{NewServerWithConfig(cfg), NewServerWithConfig(cfg)}
			ctx := context.Background()
			require.NoError(t, servers[0].apiStore.SaveRun(ctx, controlplane.Run{ID: "run", Topology: "topo", AgentID: "agent", Status: controlplane.RunAssigned}))
			type result struct {
				run *controlplane.Run
				ok  bool
				err error
			}
			results := make(chan result, 8)
			for i := 0; i < 8; i++ {
				go func(i int) {
					var outcome result
					for attempt := 0; attempt < 20; attempt++ {
						outcome.run, outcome.ok, outcome.err = servers[i%2].apiStore.ClaimRun(ctx, "run", "agent", "same-execution", time.Minute)
						if outcome.err == nil {
							break
						}
						// SQLite may reject a contended read-to-write upgrade. The API maps
						// that outcome to retryable 503; repeat exactly the same claimant.
						time.Sleep(time.Millisecond)
					}
					results <- outcome
				}(i)
			}
			var first *controlplane.Run
			for i := 0; i < 8; i++ {
				result := <-results
				require.NoError(t, result.err)
				require.True(t, result.ok)
				require.Equal(t, 1, result.run.Attempt)
				if first == nil {
					first = result.run
				} else {
					require.Equal(t, first, result.run)
				}
			}
		})
	}
}

func TestClaimLostHTTPResponseAfterCommit(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = "sqlite://" + t.TempDir() + "/state.db"
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "agent", Status: "online"}))
	require.NoError(t, s.apiStore.SaveRun(context.Background(), controlplane.Run{ID: "run", Topology: "topo", AgentID: "agent", Status: controlplane.RunAssigned}))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			committed := httptest.NewRecorder()
			s.ServeHTTP(committed, r)
			if committed.Code != 200 {
				t.Errorf("claim failed before injected disconnect: %s", committed.Body.String())
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		s.ServeHTTP(w, r)
	}))
	defer server.Close()
	post := func() (*http.Response, error) {
		return server.Client().Post(server.URL+"/v1/agents/agent/runs/run/claim", "application/json", strings.NewReader(`{"claim_id":"8e552a37-152b-4567-bb41-196fa8c81a27"}`))
	}
	_, err := post()
	require.Error(t, err)
	committed, err := s.apiStore.GetRun(context.Background(), "run")
	require.NoError(t, err)
	require.Equal(t, 1, committed.Attempt)
	response, err := post()
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	var recovered controlplane.Run
	require.NoError(t, json.NewDecoder(response.Body).Decode(&recovered))
	require.Equal(t, *committed, recovered)
}

type countedClaimBody struct {
	io.Reader
	read int
}

func (r *countedClaimBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}
func (r *countedClaimBody) Close() error { return nil }

func TestClaimBodyIsBoundedBeforeSignatureVerification(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "agent", Status: "online", AuthSecret: "fixture", SecretHash: agent.SecretHash("fixture")}))
	payload := strings.Repeat("x", 4096)
	req := httptest.NewRequest("POST", "/v1/agents/agent/runs/run/claim", strings.NewReader(payload))
	require.NoError(t, agent.SignRequest(req, "agent", "fixture", time.Now()))
	body := &countedClaimBody{Reader: strings.NewReader(payload)}
	req.Body = body
	s.ServeHTTP(httptest.NewRecorder(), req)
	require.LessOrEqual(t, body.read, 1025, "signature verification must not read an unbounded claim body")
}
func TestClaimIdentityIsBoundToAgentSignature(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "agent", Status: "online", AuthSecret: "fixture", SecretHash: agent.SecretHash("fixture")}))
	req := httptest.NewRequest("POST", "/v1/agents/agent/runs/run/claim", strings.NewReader(`{"claim_id":"8e552a37-152b-4567-bb41-196fa8c81a27"}`))
	require.NoError(t, agent.SignRequest(req, "agent", "fixture", time.Now()))
	req.Body = io.NopCloser(strings.NewReader(`{"claim_id":"8e552a37-152b-4567-bb41-196fa8c81a28"}`))
	response := httptest.NewRecorder()
	s.ServeHTTP(response, req)
	require.Equal(t, 401, response.Code)
}
