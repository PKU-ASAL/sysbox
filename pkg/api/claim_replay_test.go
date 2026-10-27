package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDurableClaimReceiptReplayAcrossJobsRestart(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		original := controlplane.Run{ID: "claim-replay", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
		require.NoError(t, store.SaveRun(context.Background(), original))
		expected := controlplane.RunClaim{QueuedAt: original.QueuedAt, ClaimID: "8e552a37-152b-4567-bb41-196fa8c81a27"}
		firstJobs := newJobsWithRecovery(t.TempDir(), store, false)
		first, err := firstJobs.claimExpected(original.ID, "agent", &expected)
		require.NoError(t, err)
		restarted := newJobsWithRecovery(t.TempDir(), store, false)
		restarted.runLeaseTTL = time.Hour
		second, err := restarted.claimExpected(original.ID, "agent", &expected)
		require.NoError(t, err)
		require.Equal(t, first, second)
		expected.ClaimID = "8e552a37-152b-4567-bb41-196fa8c81a28"
		_, err = restarted.claimExpected(original.ID, "agent", &expected)
		require.Error(t, err)
	})
}

func TestClaimHTTPReceiptReplay(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "agent", Status: "online"}))
	run := s.jobs.startWithOptions("lab", "apply", runStartOptions{AgentID: "agent"})
	require.NoError(t, s.jobs.assign(run, "agent"))
	body, err := json.Marshal(controlplane.RunClaim{QueuedAt: run.QueuedAt, Revision: run.Revision, ClaimID: "8e552a37-152b-4567-bb41-196fa8c81a27"})
	require.NoError(t, err)
	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/agents/agent/runs/"+run.ID+"/claim", bytes.NewReader(body)))
		return rec
	}
	first := request()
	require.Equal(t, 200, first.Code, first.Body.String())
	replay := request()
	require.Equal(t, 200, replay.Code, replay.Body.String())
	require.JSONEq(t, first.Body.String(), replay.Body.String())
}

type claimCountingReader struct {
	reader io.Reader
	count  int
}

func (r *claimCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += n
	return n, err
}
func TestClaimBodyBoundedBeforeSignature(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
	body := strings.Repeat("x", 65536)
	req := httptest.NewRequest("POST", "/v1/agents/agent/runs/run/claim", strings.NewReader(body))
	require.NoError(t, agent.SignRequest(req, "agent", "secret", time.Now()))
	counted := &claimCountingReader{reader: strings.NewReader(body)}
	req.Body = io.NopCloser(counted)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.LessOrEqual(t, counted.count, 1025)
	require.NotEqual(t, 200, rec.Code)
}

func TestDispatchCannotInjectCompletionProjection(t *testing.T) {
	proof := &controlplane.Projection{Serial: 999}
	require.Error(t, validateRunDispatchPayload(RunDispatchRequest{Run: controlplane.Run{CompletionProjection: proof}}))
	require.Error(t, validateRunDispatchPayload(RunDispatchRequest{Command: controlplane.AgentCommand{Run: &controlplane.Run{CompletionProjection: proof}}}))
}
