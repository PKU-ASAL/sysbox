package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestHostAuthorizationCommitsOnceAndRetainsRejectedClock(t *testing.T) {
	ctx := context.Background()
	store := &controlstore.SQLite{DbPath: filepath.Join(t.TempDir(), "api.db"), RunsDir: t.TempDir()}
	db, err := store.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := stateDeleteTestServer(t, store)
	s.agentSvc = newAgentService(s)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(key))
	secret := "authorization-test-secret"
	require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	uid := grantTestTopologyUID(t, s, "lab")
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
	session, err := authority.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusLeased, Run: &controlplane.Run{ID: "run", Topology: "lab", Op: "apply", Revision: "revision"}, TopologyUID: uid, ActionID: "action", Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute)}
	require.NoError(t, store.SaveAgentCommand(ctx, cmd))
	grant, err := s.issueCommandGrant(ctx, "agent", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: "worker", Attempt: 1, BootID: "boot"})
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(key, *grant, "boot", time.Now())
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE authority_writes(n INTEGER); INSERT INTO authority_writes VALUES(0); CREATE TRIGGER count_authority_write AFTER UPDATE ON sysbox_execution_authority BEGIN UPDATE authority_writes SET n=n+1; END`)
	require.NoError(t, err)
	post := func(proof controlplane.SignedExecutionGrant) *httptest.ResponseRecorder {
		raw, err := json.Marshal(proof)
		require.NoError(t, err)
		req := httptest.NewRequest("POST", "/v1/agents/agent/execution-grants/authorize", bytes.NewReader(raw))
		require.NoError(t, agent.SignRequest(req, "agent", secret, time.Now()))
		out := httptest.NewRecorder()
		s.ServeHTTP(out, req)
		return out
	}
	out := post(signed)
	require.Equal(t, 200, out.Code, out.Body.String())
	var writes int
	require.NoError(t, db.QueryRow("SELECT n FROM authority_writes").Scan(&writes))
	require.Equal(t, 1, writes, "valid authorization must commit its clock and validation together")
	for _, reason := range []string{"bad-signature", "expired", "missing-command"} {
		t.Run(reason, func(t *testing.T) {
			proof := signed
			if reason == "bad-signature" {
				proof.Signature = []byte("invalid")
			} else if reason == "expired" {
				expired := signed.Grant
				expired.ExpiresAt = time.Now().Add(-time.Second)
				var err error
				proof, err = controlplane.SignExecutionGrant(key, expired, "boot", expired.ExpiresAt.Add(-time.Minute))
				require.NoError(t, err)
			} else {
				require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID))
			}
			before := time.Now().UTC()
			out := post(proof)
			require.Equal(t, 409, out.Code, out.Body.String())
			raw, err := (controlstore.AuthorityStorage{Store: store}).ReadExecutionAuthority(ctx)
			require.NoError(t, err)
			var clock struct {
				ObservedAt time.Time `json:"observed_at"`
			}
			require.NoError(t, json.Unmarshal(raw, &clock))
			require.False(t, clock.ObservedAt.Before(before))
		})
	}
}

func TestRejectedAuthorizationPersistsClockAfterRequestCancellation(t *testing.T) {
	store := &controlstore.Local{RunsDir: t.TempDir()}
	storage := controlstore.AuthorityStorage{Store: store}
	repo := persistence.NewExecutionGrantRepository(storage)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := time.Now().UTC()
	require.NoError(t, observeRejectedAuthorization(ctx, repo))
	raw, err := storage.ReadExecutionAuthority(context.Background())
	require.NoError(t, err)
	var clock struct {
		ObservedAt time.Time `json:"observed_at"`
	}
	require.NoError(t, json.Unmarshal(raw, &clock))
	require.False(t, clock.ObservedAt.Before(before))
	grants, err := repo.ListActiveGrants(context.Background())
	require.NoError(t, err)
	require.Empty(t, grants)
}
