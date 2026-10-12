package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDerivedSSHGrantCompletionBindsExactRequestAndParent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	_, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPublic, host, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
	s.grantHostKeys = map[string]ed25519.PublicKey{"agent": hostPublic}
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
	repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := repo.OpenSession(ctx, "agent", "worker", "")
	require.NoError(t, err)
	run := controlplane.Run{ID: "run", Topology: "lab", Revision: "revision", Op: "apply"}
	cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusLeased, Run: &run, TopologyUID: grantTestTopologyUID(t, s, "lab"), ActionID: "run", Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute)}
	require.NoError(t, s.apiStore.SaveAgentCommand(ctx, cmd))
	g, err := s.sourceGrant(ctx, "agent", ExecutionGrantRequest{SessionID: session.ID, Attempt: 1, LeaseOwner: "worker", BootID: "host"}, &persistence.CommandRecord{Command: cmd})
	require.NoError(t, err)
	parent, err := repo.Issue(ctx, g, time.Minute)
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(issuer, *parent, "host", time.Now())
	require.NoError(t, err)
	mutation := substrate.SSHAccessMutation{ID: "ssh", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	post := func(path string, body any, auth bool) *httptest.ResponseRecorder {
		raw, e := json.Marshal(body)
		require.NoError(t, e)
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		if auth {
			require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := post("/v1/agents/agent/execution-grant/derive-ssh", map[string]any{"parent": signed, "request": mutation, "resource_uid": "node", "generation": 1, "guest_boot_id": "guest"}, true)
	require.Equal(t, http.StatusUnauthorized, w.Code, "Agent authentication and parent possession alone cannot authorize arbitrary SSH configuration")
	proof, err := controlplane.SignSSHGrantDerivation(host, controlplane.SSHGrantDerivation{Parent: signed, Request: mutation, ResourceUID: "node", Generation: 1, GuestBootID: "guest"})
	require.NoError(t, err)
	changed := proof
	changed.Request.Config.User = "root"
	w = post("/v1/agents/agent/execution-grant/derive-ssh", changed, true)
	require.Equal(t, http.StatusUnauthorized, w.Code, "worker cannot substitute the canonical host-selected account")
	w = post("/v1/agents/agent/execution-grant/derive-ssh", proof, true)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var child controlplane.SignedExecutionGrant
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &child))
	require.Equal(t, "guest_ssh_access_delete", child.Grant.Operation)
	require.Equal(t, parent.Epoch, child.Grant.ParentEpoch)
	result := substrate.SSHAccessResult{Handle: substrate.SSHAccessHandle{ID: "ssh", Digest: child.Grant.Digest, NodeID: "web", User: "ubuntu", AccessID: "admin", Revision: 2, BootID: "guest", Epoch: child.Grant.Epoch, TopologyUID: cmd.TopologyUID, ResourceUID: "node", Generation: 1}}
	receipt, err := controlplane.SignGuestSSHAccessStopReceipt(host, child.Grant, "host", mutation, result, "receipt", time.Now())
	require.NoError(t, err)
	path := "/v1/hosts/agent/derived-ssh/ssh/completed"
	bad := mutation
	bad.ExpectedRevision++
	w = post(path, map[string]any{"receipt": receipt, "request": bad}, false)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	forged := receipt
	forged.Grant.ParentEpoch++
	forged, err = controlplane.SignGuestSSHAccessStopReceipt(host, forged.Grant, "host", mutation, result, "forged", time.Now())
	require.NoError(t, err)
	w = post(path, map[string]any{"receipt": forged, "request": mutation}, false)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	for i := 0; i < 2; i++ {
		w = post(path, map[string]any{"receipt": receipt, "request": mutation}, false)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	require.NoError(t, repo.AcceptHostStop(ctx, *parent, "parent-complete"))
}
