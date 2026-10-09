package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func grantTestTopologyUID(t *testing.T, s *Server, topology string) string {
	t.Helper()
	repo, err := s.actionRepository(topology)
	require.NoError(t, err)
	st, err := repo.LoadState(context.Background(), topology)
	require.NoError(t, err)
	return st.TopologyUID
}

func TestGrantIssuanceRejectsCommandForDeletedLifecycle(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		_, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(key))
		secret := "test-agent-secret"
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		before, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusLeased, Run: &controlplane.Run{ID: "run", Topology: "lab", Op: "apply", Revision: "revision"}, TopologyUID: before.TopologyUID, ActionID: "action", Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute)}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "boot"}
		request := func() *httptest.ResponseRecorder {
			raw, err := json.Marshal(proof)
			require.NoError(t, err)
			r := httptest.NewRequest("POST", "/v1/agents/agent/commands/command/execution-grant", bytes.NewReader(raw))
			require.NoError(t, agent.SignRequest(r, "agent", secret, time.Now()))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			return w
		}
		first := request()
		require.Equal(t, 200, first.Code, first.Body.String())
		var signed controlplane.SignedExecutionGrant
		require.NoError(t, json.Unmarshal(first.Body.Bytes(), &signed))
		require.NoError(t, authority.AcceptHostStop(ctx, signed.Grant, "verified-stop"))
		require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", before.Meta.Serial, before.TopologyUID), persistence.ErrConflict, "live delivery still excludes API deletion")
		// Simulate an out-of-band lifecycle rebuild so this test continues to
		// exercise grant fencing independently of the stronger API delete gate.
		raw, err := (apiExecutionGrantStorage{store: store}).ReadExecutionAuthority(ctx)
		require.NoError(t, err)
		require.NoError(t, repo.DeleteQuiescentLifecycle(ctx, "lab", before.Meta.Serial, before.TopologyUID, raw))
		// The delivery lease still exists. A fresh epoch must not resurrect the
		// old topology incarnation merely because its earlier grant stopped.
		late := request()
		require.Equal(t, 409, late.Code, late.Body.String())
		grants, err := authority.ListActiveGrants(ctx)
		require.NoError(t, err)
		require.Empty(t, grants)
	})
}

func TestGrantAndMetadataDeleteCannotBothCommit(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		for attempt := 0; attempt < 8; attempt++ {
			st, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			id := fmt.Sprintf("command-%d", attempt)
			cmd := controlplane.AgentCommand{ID: id, AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusLeased, Run: &controlplane.Run{ID: id, Topology: "lab", Op: "apply", Revision: "revision"}, TopologyUID: st.TopologyUID, ActionID: id, Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute)}
			require.NoError(t, store.SaveAgentCommand(ctx, cmd))
			proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: 1, BootID: "boot"}
			var grant *controlplane.ExecutionGrant
			var grantErr, deleteErr error
			start := make(chan struct{})
			var workers sync.WaitGroup
			workers.Add(2)
			go func() { defer workers.Done(); <-start; grant, grantErr = s.issueCommandGrant(ctx, "agent", id, proof) }()
			go func() {
				defer workers.Done()
				<-start
				deleteErr = s.deleteStateWithAuthority(ctx, "lab", st.Meta.Serial, st.TopologyUID)
			}()
			close(start)
			workers.Wait()
			require.NoError(t, grantErr)
			require.ErrorIs(t, deleteErr, persistence.ErrConflict, "delete cannot overtake a live command or its grant")
			require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"))
			require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", st.Meta.Serial, st.TopologyUID), persistence.ErrConflict)
			require.NoError(t, store.DeleteAgentCommand(ctx, "agent", id))
			require.NoError(t, s.deleteStateWithAuthority(ctx, "lab", st.Meta.Serial, st.TopologyUID))
		}
	})
}

func TestRejectedGrantOnRetiredLifecycleStillAdvancesClock(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		uid := grantTestTopologyUID(t, s, "lab")
		storage := apiExecutionGrantStorage{store: store}
		authority := persistence.NewExecutionGrantRepository(storage)
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		cmd := controlplane.AgentCommand{ID: "expired", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusLeased, TopologyUID: uid, ActionID: "action", Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(-time.Minute), Run: &controlplane.Run{ID: "run", Topology: "lab", Op: "apply", Revision: "revision"}}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		require.ErrorIs(t, s.deleteStateWithAuthority(ctx, "lab", 0, uid), persistence.ErrConflict)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		rawAuthority, err := storage.ReadExecutionAuthority(ctx)
		require.NoError(t, err)
		// Keep the expired command for the clock/fencing check after an external
		// rebuild, which normal API deletion now refuses while it is unresolved.
		require.NoError(t, repo.DeleteQuiescentLifecycle(ctx, "lab", 0, uid, rawAuthority))
		before := time.Now().UTC()
		grant, err := s.issueCommandGrant(ctx, "agent", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: "worker", Attempt: 1, BootID: "boot"})
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.Nil(t, grant)
		raw, err := storage.ReadExecutionAuthority(ctx)
		require.NoError(t, err)
		var clock struct {
			ObservedAt time.Time `json:"observed_at"`
		}
		require.NoError(t, json.Unmarshal(raw, &clock))
		require.False(t, clock.ObservedAt.Before(before), "rejected expired source must still persist its time observation")
	})
}
