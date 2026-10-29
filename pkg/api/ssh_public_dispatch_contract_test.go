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

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// Start at public admission, not a manually seeded guest operation. A command
// accepted here must be consumable by the production issuer and Agent decoder.
func TestPublicSSHAdmissionProducesIssuableGuestCommand(t *testing.T) {
	s := guestExecutionTestServer(t)
	writeState(t, s.cfg.Paths.RunsDir, "lab", &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{
		Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker",
		Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []map[string]any{{"User": "ubuntu"}}}}),
	}}})
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"operation": "ssh_access_apply", "id": "public-ssh", "access_id": "admin", "node": "web",
		"authorized_keys": []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}})
	require.NoError(t, err)
	r := httptest.NewRequest("POST", "/v1/topologies/lab/ssh-access", bytes.NewReader(body))
	r.Header.Set("X-Sysbox-Roles", "admin")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, 202, w.Code, w.Body.String())
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(context.Background(), "host-a", commands[0].ID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = controlplane.FromGuestSSHAccessCommand(*cmd)
	require.NoError(t, err, "publicly accepted payload must satisfy the actual guest command contract")
	inventory, err := s.apiStore.GetAgentInventory(context.Background(), "host-a")
	require.NoError(t, err)
	grant, err := s.sourceGrant(context.Background(), "host-a", ExecutionGrantRequest{SessionID: inventory.SessionID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "guest"}, &persistence.CommandRecord{Command: *cmd})
	require.NoError(t, err)
	issued, err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).Issue(context.Background(), grant, time.Minute)
	require.NoError(t, err)
	hostPublic, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"host-a": hostPublic}
	mutation := *cmd.Operation.SSHMutation
	result := substrate.SSHAccessResult{Present: true, Handle: substrate.SSHAccessHandle{NodeID: "web", ID: mutation.ID, Digest: issued.Digest, User: "ubuntu", AccessID: "admin", Revision: 1, BootID: "guest", TopologyUID: issued.Scope.TopologyUID, ResourceUID: issued.Scope.ResourceUID, Generation: issued.Scope.Generation, Epoch: issued.Epoch}}
	receipt, err := controlplane.SignGuestSSHAccessStopReceipt(hostPrivate, *issued, "host", mutation, result, "receipt", time.Now())
	require.NoError(t, err)
	raw, err := json.Marshal(HostNodeOperationCompletion{Receipt: receipt})
	require.NoError(t, err)
	completed := httptest.NewRecorder()
	s.ServeHTTP(completed, httptest.NewRequest("POST", "/v1/hosts/host-a/node-operations/public-ssh/completed", bytes.NewReader(raw)))
	require.Equal(t, 200, completed.Code, completed.Body.String())
	_, err = s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err, "completed public operations must not corrupt outbox seed validation")
}

func TestPublicSSHAdmissionRejectsUserOverrideAndSupportsCASDelete(t *testing.T) {
	for _, scenario := range []string{"override", "delete", "zero-revision", "invalid-key"} {
		t.Run(scenario, func(t *testing.T) {
			s := guestExecutionTestServer(t)
			writeState(t, s.cfg.Paths.RunsDir, "lab", &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{
				Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker",
				Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []map[string]any{{"User": "ubuntu"}}}}),
			}}})
			body := map[string]any{"operation": "ssh_access_delete", "id": "public-ssh", "access_id": "admin", "node": "web", "expected_revision": 3}
			if scenario == "override" {
				body["user"] = "root"
				pub, _, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
				key, err := ssh.NewPublicKey(pub)
				require.NoError(t, err)
				body["operation"] = "ssh_access_apply"
				body["authorized_keys"] = []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
			}
			if scenario == "zero-revision" {
				body["expected_revision"] = 0
			}
			if scenario == "invalid-key" {
				body["operation"] = "ssh_access_apply"
				body["authorized_keys"] = []string{"invalid"}
			}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			r := httptest.NewRequest("POST", "/v1/topologies/lab/ssh-access", bytes.NewReader(raw))
			r.Header.Set("X-Sysbox-Roles", "admin")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
			require.NoError(t, err)
			if scenario != "delete" {
				require.Equal(t, 400, w.Code, w.Body.String())
				require.Empty(t, commands)
				return
			}
			require.Equal(t, 202, w.Code, w.Body.String())
			require.Len(t, commands, 1)
			mutation, err := controlplane.FromGuestSSHAccessCommand(commands[0])
			require.NoError(t, err)
			require.True(t, mutation.Delete)
			require.Equal(t, uint64(3), mutation.ExpectedRevision)
			require.Equal(t, "ubuntu", mutation.Config.User)
			require.Empty(t, mutation.Config.AuthorizedKeys)
		})
	}
}

func TestSSHAdmissionReadsCanonicalProviderUser(t *testing.T) {
	resource := state.Resource{Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"provider_config": map[string]any{"SSHUser": "debian"}}})}
	user, err := canonicalSSHUserFromResource(resource)
	require.NoError(t, err)
	require.Equal(t, "debian", user)
}
