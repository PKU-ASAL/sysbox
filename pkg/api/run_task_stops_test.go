package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestRunTaskStopDiscoveryChecksRetiredParentSource(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(key))
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "agent", "host", "")
		require.NoError(t, err)
		for _, good := range []bool{true, false} {
			topology := "lab"
			if !good {
				topology = "broken"
			}
			run := controlplane.Run{ID: topology + "-run", Topology: topology, Revision: "revision", AgentID: "agent", Op: "apply", Operation: "apply", Status: controlplane.RunRunning, Target: "sysbox_node.web"}
			cmd := controlplane.AgentCommand{ID: topology + "-command", Type: "run_assigned", AgentID: "agent", TopologyUID: topology + "-uid", ActionID: topology + "-action", Run: &run, Attempt: 1, ResourceUID: "node", Generation: 1}
			require.NoError(t, store.SaveAgentCommand(ctx, cmd))
			req := hostsupervisor.RunRequest{RunID: run.ID, Topology: topology, TopologyUID: cmd.TopologyUID, Revision: run.Revision, ActionID: cmd.ActionID, Action: "apply", Target: run.Target, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}
			digest, err := req.Digest()
			require.NoError(t, err)
			if !good {
				digest = "wrong-parent-digest"
			}
			parent, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID}, AgentID: "agent", SessionID: session.ID, CommandID: cmd.ID, ActionID: cmd.ActionID, Attempt: 1, Operation: "run_apply", Digest: digest, TargetBootID: "host"}, time.Minute)
			require.NoError(t, err)
			_, err = authority.DeriveGuestTask(ctx, *parent, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: cmd.ID, ActionID: topology + "-task", Attempt: 1, Operation: "guest_task", Digest: "task-digest", TargetBootID: "guest"}, time.Minute)
			require.NoError(t, err)
			require.NoError(t, authority.Revoke(ctx, *parent))
			renewed, acquired, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "replacement-worker", time.Minute)
			require.NoError(t, err)
			require.True(t, acquired)
			require.Greater(t, uint64(renewed.Attempt), parent.Attempt, "historical child remains tied to original parent attempt")
			require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID))
		}
		r := httptest.NewRequest("POST", "/v1/agents/agent/guest-task-stops", bytes.NewBufferString(`{}`))
		require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		require.Equal(t, 200, w.Code, w.Body.String())
		var page controlplane.GuestTaskStopPage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
		require.Len(t, page.Stops, 1)
		require.Len(t, page.Issues, 1)
		stop := page.Stops[0]
		require.Equal(t, "lab", stop.Topology)
		require.Empty(t, stop.Node, "host derives the node from canonical Run candidates")
		require.Equal(t, "lab-run", stop.Cancellation.RunID)
		require.Equal(t, "lab", stop.Cancellation.Topology)
		require.NoError(t, controlplane.VerifyExecutionCancellation(pub, stop.Cancellation))
	})
}
