package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGuestTaskStopDiscoveryRequiresOwnerAndRevocation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(key))
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
		x, cmd, digest := admitGuestStopCandidate(t, store, "agent", 0)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "agent", "host", "")
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: cmd.ID, ActionID: x.ID, Attempt: 1, Operation: "guest_task", Digest: digest, TargetBootID: "guest"}, time.Minute)
		require.NoError(t, err)
		post := func(signed bool) *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", "/v1/agents/agent/guest-task-stops", bytes.NewBufferString(`{}`))
			if signed {
				require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			return w
		}
		require.Equal(t, 401, post(false).Code)
		w := post(true)
		require.Equal(t, 200, w.Code, w.Body.String())
		var page controlplane.GuestTaskStopPage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
		require.Empty(t, page.Stops)
		require.NoError(t, authority.Revoke(ctx, *grant))
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID))
		w = post(true)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
		require.Len(t, page.Stops, 1)
		stop := page.Stops[0]
		require.Equal(t, x.Topology, stop.Topology)
		require.Equal(t, x.Node, stop.Node)
		require.Equal(t, grant.Epoch, stop.Cancellation.Grant.Epoch)
		require.NoError(t, controlplane.VerifyExecutionCancellation(pub, stop.Cancellation))
		blocked, err := store.GetAgent(ctx, "agent")
		require.NoError(t, err)
		blocked.Disabled = true
		require.NoError(t, store.SaveAgent(ctx, *blocked))
		require.Equal(t, 200, post(true).Code, "blocked owner can still drain effects")
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "other", AuthSecret: "other-secret", SecretHash: agent.SecretHash("other-secret")}))
		r := httptest.NewRequest("POST", "/v1/agents/other/guest-task-stops", bytes.NewBufferString(`{}`))
		require.NoError(t, agent.SignRequest(r, "other", "other-secret", time.Now()))
		other := httptest.NewRecorder()
		s.ServeHTTP(other, r)
		require.Equal(t, 200, other.Code)
		var otherPage controlplane.GuestTaskStopPage
		require.NoError(t, json.Unmarshal(other.Body.Bytes(), &otherPage))
		require.Empty(t, otherPage.Stops)
	})
}

func TestGuestTaskStopDiscoveryPagesPastBadSourceWithStableCeiling(t *testing.T) {
	ctx := context.Background()
	store := &controlstore.Local{RunsDir: t.TempDir()}
	s := stateDeleteTestServer(t, store)
	s.agentSvc = newAgentService(s)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(key))
	require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
	session, err := authority.OpenSession(ctx, "agent", "host", "")
	require.NoError(t, err)
	create := func(index int, admitted bool) *controlplane.ExecutionGrant {
		var id, digest string
		if admitted {
			x, _, d := admitGuestStopCandidate(t, store, "agent", index)
			id, digest = x.ID, d
		} else {
			id, digest = "missing-source", "digest"
		}
		g, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: id, Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: id, ActionID: id, Attempt: 1, Operation: "guest_task", Digest: digest, TargetBootID: "guest"}, time.Minute)
		require.NoError(t, err)
		require.NoError(t, authority.Revoke(ctx, *g))
		return g
	}
	create(0, false)
	for i := 1; i <= controlplane.GuestTaskStopPageSize; i++ {
		create(i, true)
	}
	fetch := func(cursor controlplane.GuestTaskStopCursor) controlplane.GuestTaskStopPage {
		raw, err := json.Marshal(cursor)
		require.NoError(t, err)
		r := httptest.NewRequest("POST", "/v1/agents/agent/guest-task-stops", bytes.NewReader(raw))
		require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		require.Equal(t, 200, w.Code, w.Body.String())
		var page controlplane.GuestTaskStopPage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
		return page
	}
	first := fetch(controlplane.GuestTaskStopCursor{})
	require.Len(t, first.Issues, 1)
	require.Len(t, first.Stops, controlplane.GuestTaskStopPageSize-1)
	require.NotZero(t, first.NextEpoch)
	later := create(controlplane.GuestTaskStopPageSize+1, true)
	last := fetch(controlplane.GuestTaskStopCursor{AfterEpoch: first.NextEpoch, ThroughEpoch: first.ThroughEpoch})
	require.Len(t, last.Stops, 1)
	require.Zero(t, last.NextEpoch)
	require.Equal(t, first.ThroughEpoch, last.ThroughEpoch)
	require.Less(t, last.Stops[0].Cancellation.Grant.Epoch, later.Epoch)
}

func TestGuestTaskStopDiscoveryRejectsMalformedCursor(t *testing.T) {
	store := &controlstore.Local{RunsDir: t.TempDir()}
	s := stateDeleteTestServer(t, store)
	s.agentSvc = newAgentService(s)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(key))
	require.NoError(t, store.SaveAgent(context.Background(), controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
	for _, body := range []string{`{} {}`, `{} trailing`, `{"unknown":1}`, `{"after_epoch":2,"through_epoch":1}`} {
		t.Run(body, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/agents/agent/guest-task-stops", bytes.NewBufferString(body))
			require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			require.Equal(t, 400, w.Code, w.Body.String())
		})
	}
}

func admitGuestStopCandidate(t *testing.T, store controlstore.Store, owner string, index int) (controlplane.GuestExecution, controlplane.AgentCommand, string) {
	t.Helper()
	id := fmt.Sprintf("task-%s-%d", owner, index)
	x := controlplane.GuestExecution{ID: id, CommandID: id, AgentID: owner, Topology: "lab", Node: fmt.Sprintf("node-%d", index), Status: "queued", Version: 1, CreatedAt: time.Now().UTC(), Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
	cmd := controlplane.AgentCommand{ID: id, AgentID: owner, Type: "guest_execution", Execution: &x, ExecutionRequest: x.Request, ActionID: id, TopologyUID: "topology", ResourceUID: id, Generation: 1}
	require.NoError(t, store.CreateGuestExecutionDispatch(context.Background(), x, cmd))
	task, err := guesttask.FromExecutionCommand(cmd)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	return x, cmd, digest
}
