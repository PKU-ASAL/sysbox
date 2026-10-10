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

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGuestSSHStopCannotBypassCanonicalCompletion(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "session", "")
		require.NoError(t, err)
		req := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 2, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
		digest, err := req.Digest()
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: "command", ActionID: req.ID, Attempt: 1, Operation: "guest_ssh_access_delete", Digest: digest, TargetBootID: "guest"}, time.Minute)
		require.NoError(t, err)
		result := substrate.SSHAccessResult{Handle: substrate.SSHAccessHandle{ID: req.ID, Digest: digest, NodeID: "web", User: "ubuntu", AccessID: "admin", Revision: 3, BootID: "guest", TopologyUID: "owner", ResourceUID: "node", Generation: 1, Epoch: grant.Epoch}}
		receipt, err := controlplane.SignGuestSSHAccessStopReceipt(key, *grant, "host", req, result, "receipt", time.Now())
		require.NoError(t, err)
		raw, err := json.Marshal(receipt)
		require.NoError(t, err)
		response := httptest.NewRecorder()
		s.ServeHTTP(response, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
		require.Equal(t, 409, response.Code, response.Body.String())
		require.NoError(t, authority.Authorize(ctx, *grant), "scope must remain held until canonical SSH result is committed")
	})
}
