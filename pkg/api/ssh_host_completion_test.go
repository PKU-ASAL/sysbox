package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSHHostCompletionBindsEvidenceAndSurvivesRetiredCommand(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		public, err := ssh.NewPublicKey(pub)
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "session", "")
		require.NoError(t, err)
		for _, action := range []string{"ssh_access_apply", "ssh_access_delete"} {
			req := controlplane.SSHAccessRequest{ID: action, AccessID: "admin", Topology: "lab", Node: "web", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))}}
			digest, err := req.Digest()
			require.NoError(t, err)
			op := prepareNodeOperation(controlplane.NodeOperation{ID: req.ID, AgentID: "agent", Topology: "lab", Node: "web", Operation: action, SSHAccess: &req, ResourceUID: "node", Generation: 1})
			op.CommandID = "command-" + action
			op.ActionID = req.ID
			require.NoError(t, store.CreateNodeOperation(ctx, op))
			command := controlplane.AgentCommand{ID: op.CommandID, AgentID: "agent", Type: "node_operation", ActionID: req.ID, TopologyUID: "owner-uid", ResourceUID: "node", Generation: 1, Operation: op, Attempt: 1, LeaseOwner: "expired-worker", LeaseUntil: time.Now().Add(-time.Hour)}
			require.NoError(t, store.SaveAgentCommand(ctx, command))
			grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner-uid", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: command.ID, ActionID: req.ID, Attempt: 1, Operation: action, Digest: digest, TargetBootID: "host"}, time.Minute)
			require.NoError(t, err)
			content, err := req.AuthorizedKeysContent()
			require.NoError(t, err)
			evidence := map[string]any{"access_id": req.AccessID, "sha256": fmt.Sprintf("%x", sha256.Sum256(content))}
			if action == "ssh_access_delete" {
				evidence = map[string]any{"access_id": req.AccessID, "deleted": true}
			}
			raw, err := json.Marshal(evidence)
			require.NoError(t, err)
			receipt, err := controlplane.SignSSHAccessStopReceipt(key, *grant, "host", "receipt-"+action, raw, time.Now())
			require.NoError(t, err)
			post := func(data json.RawMessage) *httptest.ResponseRecorder {
				body, err := json.Marshal(HostNodeOperationCompletion{Receipt: receipt, Evidence: data})
				require.NoError(t, err)
				out := httptest.NewRecorder()
				s.ServeHTTP(out, httptest.NewRequest("POST", "/v1/hosts/agent/node-operations/"+op.ID+"/completed", bytes.NewReader(body)))
				return out
			}
			require.NoError(t, store.DeleteAgentCommand(ctx, "agent", command.ID))
			stopBody, err := json.Marshal(receipt)
			require.NoError(t, err)
			generic := httptest.NewRecorder()
			s.ServeHTTP(generic, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(stopBody)))
			require.Equal(t, 409, generic.Code, "generic stop cannot bypass operation result commit")
			require.Equal(t, 409, post(json.RawMessage(`{"access_id":"forged"}`)).Code)
			original := receipt
			for _, bad := range []json.RawMessage{json.RawMessage(`{"access_id":"other","sha256":"wrong","deleted":true}`), json.RawMessage(`{"access_id":"admin","sha256":"wrong","deleted":false}`)} {
				receipt, err = controlplane.SignSSHAccessStopReceipt(key, *grant, "host", "invalid-result", bad, time.Now())
				require.NoError(t, err)
				require.Equal(t, 409, post(bad).Code, "a valid signature cannot bless inconsistent SSH evidence")
			}
			receipt = original
			for range 2 {
				response := post(raw)
				require.Equal(t, 200, response.Code, response.Body.String())
			}
			done, err := store.GetNodeOperation(ctx, op.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.NodeOperationStatusDone, done.Status)
			require.Error(t, authority.Authorize(ctx, *grant))
		}
	})
}
