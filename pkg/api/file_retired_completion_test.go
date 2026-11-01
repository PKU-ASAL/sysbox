package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFileHostCompletionAfterRealRetirement(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		op := controlplane.GuestFileOperation{ID: "install", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", CommandID: "command", Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now().UTC()}
		cmd := controlplane.AgentCommand{ID: op.CommandID, AgentID: op.AgentID, Type: "guest_file_put", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, ActionID: op.ID, FilePut: &controlplane.GuestFilePut{ID: op.ID, Topology: op.Topology, Node: op.Node, Path: "/opt/files/data", Mode: 0600, Size: 3, SHA256: strings.Repeat("a", 64)}}
		require.NoError(t, store.CreateGuestFileDispatch(ctx, op, cmd))
		lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "agent", "first", "")
		require.NoError(t, err)
		request, err := controlplane.FromFileInstallCommand(cmd)
		require.NoError(t, err)
		digest, err := request.Digest()
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}, AgentID: cmd.AgentID, SessionID: session.ID, CommandID: cmd.ID, ActionID: op.ID, Attempt: uint64(lease.Attempt), Operation: "guest_file_install", TargetBootID: "guest", Digest: digest}, time.Minute)
		require.NoError(t, err)
		result := substrate.FileInstallResult{Outcome: substrate.FileInstallCommitted, Quiescent: true, SHA256: request.SHA256, Size: request.Size, Mode: request.Mode}
		receipt, err := controlplane.SignExecutionFileStopReceipt(key, *grant, "host", "guest", op.ID, digest, result, "receipt", time.Now())
		require.NoError(t, err)
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID))
		retired, err := readFailureCommand(ctx, store, cmd.ID)
		require.NoError(t, err)
		require.True(t, retired.Retired)
		require.Nil(t, retired.Command.FilePut, "real retirement scrubs dispatch payload")
		post := func(r controlplane.SignedExecutionStopReceipt) int {
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
			return w.Code
		}
		badResult := result
		badResult.Size++
		bad, err := controlplane.SignExecutionFileStopReceipt(key, *grant, "host", "guest", op.ID, digest, badResult, "bad", time.Now())
		require.NoError(t, err)
		require.Equal(t, 409, post(bad), "signed receipt cannot substitute its own size for the admitted FilePut")
		require.Equal(t, 200, post(receipt), "valid historical host result must reconstruct the durable dispatch seed")
		completed, err := store.GetGuestFileOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.GuestExecutionCompleted, completed.Status)
		require.Equal(t, result, *completed.Result)
		require.Equal(t, 200, post(receipt), "identical receipt replay after command retirement must be idempotent")
	})
}
