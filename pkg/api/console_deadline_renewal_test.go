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
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestConsoleRenewalRespectsAbsoluteDeadline(t *testing.T) {
	for _, seconds := range []int{10, 120} {
		t.Run((time.Duration(seconds) * time.Second).String(), func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
				ctx := context.Background()
				s := stateDeleteTestServer(t, store)
				s.agentSvc = nil
				_, key, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
				require.NoError(t, s.ConfigureExecutionGrantSigner(key))
				require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
				sess, cmd := consoleDispatchFixture()
				sess.TopologyUID = grantTestTopologyUID(t, s, "lab")
				sess.ResourceUID, sess.Generation = "node-uid", 2
				cmd.TopologyUID, cmd.ResourceUID, cmd.Generation, cmd.ActionID = sess.TopologyUID, sess.ResourceUID, sess.Generation, sess.ID
				cmd.Session = &sess
				cmd.Request.TimeoutSeconds = seconds
				require.NoError(t, store.CreateConsoleDispatch(ctx, sess, cmd))
				lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
				require.NoError(t, err)
				require.True(t, ok)
				repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
				session, err := repo.OpenSession(ctx, "agent", "process", "")
				require.NoError(t, err)
				grant, err := s.issueCommandGrant(ctx, "agent", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt, BootID: "guest"})
				require.NoError(t, err)
				signed, err := controlplane.SignExecutionGrant(key, *grant, "guest", time.Now())
				require.NoError(t, err)
				body, err := json.Marshal(ExecutionGrantAuthorization{Grant: signed, LeaseOwner: lease.LeaseOwner})
				require.NoError(t, err)
				req := httptest.NewRequest("POST", "/v1/agents/agent/commands/"+cmd.ID+"/execution-grant/renew", bytes.NewReader(body))
				require.NoError(t, agent.SignRequest(req, "agent", "secret", time.Now()))
				w := httptest.NewRecorder()
				s.mux.ServeHTTP(w, req)
				require.Equal(t, 200, w.Code, w.Body.String())
				var renewed controlplane.SignedExecutionGrant
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &renewed))
				require.False(t, renewed.Grant.ExpiresAt.After(sess.CreatedAt.Add(time.Duration(seconds)*time.Second)))
			})
		})
	}
}
