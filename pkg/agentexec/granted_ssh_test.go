package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestSSHAccessCommandRequiresBoundPayload(t *testing.T) {
	cmd := controlplane.AgentCommand{Type: "node_operation", ID: "command", AgentID: "agent", TopologyUID: "topology", ActionID: "action", ResourceUID: "resource", Generation: 1, Operation: controlplane.NodeOperation{Operation: "ssh_access_apply", SSHAccess: &controlplane.SSHAccessRequest{ID: "different", Topology: "lab", Node: "web", AuthorizedKeys: []string{"ssh-ed25519 AAAA key"}}}}
	_, err := controlplane.FromSSHAccessCommand(cmd)
	require.Error(t, err)
}

func TestSSHAgentRejectsSignedGrantForAnotherDelivery(t *testing.T) {
	for _, field := range []string{"agent", "attempt", "command", "session", "scope", "digest"} {
		t.Run(field, func(t *testing.T) {
			public, private, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			dir := t.TempDir()
			keyfile := filepath.Join(dir, "issuer")
			require.NoError(t, os.WriteFile(keyfile, public, 0600))
			mutation := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
			cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", TopologyUID: "topology", ActionID: "delete", ResourceUID: "resource", Generation: 1, Attempt: 1, LeaseOwner: "worker", Operation: controlplane.NodeOperation{ID: "delete", ActionID: "delete", AgentID: "agent", CommandID: "command", Topology: "lab", Node: "web", ResourceUID: "resource", Generation: 1, Operation: "guest_ssh_access_delete", Status: controlplane.NodeOperationStatusQueued, SSHMutation: &mutation}}
			digest, err := mutation.Digest()
			require.NoError(t, err)
			g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "delete", CommandID: "command", Attempt: 1, Epoch: 1, Operation: cmd.Operation.Operation, Digest: digest, TargetBootID: "guest", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
			switch field {
			case "agent":
				g.AgentID = "other"
			case "attempt":
				g.Attempt++
			case "command":
				g.CommandID = "other"
			case "session":
				g.SessionID = "other"
			case "scope":
				g.Scope.Generation++
			case "digest":
				g.Digest = "other"
			}
			grant, err := controlplane.SignExecutionGrant(private, g, "guest", time.Now())
			require.NoError(t, err)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(grant) }))
			defer endpoint.Close()
			socket := filepath.Join(dir, "host.sock")
			listener, err := net.Listen("unix", socket)
			require.NoError(t, err)
			done := make(chan struct{})
			var mutations atomic.Int32
			go func() {
				defer close(done)
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					var request hostsupervisor.Request
					if json.NewDecoder(conn).Decode(&request) == nil {
						response := hostsupervisor.Response{Version: 1}
						if request.Operation == "ssh-discover" {
							response.SSHDiscovery = &substrate.SSHAccessDiscovery{Query: substrate.SSHAccessQuery{NodeID: "web", User: "ubuntu", AccessID: "admin", TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, BootID: "guest", Status: "absent"}
						} else {
							mutations.Add(1)
						}
						json.NewEncoder(conn).Encode(response)
					}
					conn.Close()
				}
			}()
			defer func() { listener.Close(); <-done }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			op := executeGrantedNodeOperation(ctx, Options{ID: "agent", APIURL: endpoint.URL, ExecutionSessionID: "session", ExecutionGrantPublicKeyFile: keyfile, HostSupervisorSocket: socket}, cmd)
			require.Equal(t, controlplane.NodeOperationStatusFailed, op.Status)
			require.Contains(t, op.Err, controlplane.ErrInvalidExecutionGrant.Error())
			require.Zero(t, mutations.Load())
		})
	}
}

func TestSSHWorkerDoesNotPostCanonicalCompletionOnHostFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	for _, operation := range []string{"guest_ssh_access_apply", "guest_ssh_access_delete"} {
		runner := newCommandRunner(Options{APIURL: server.URL, ID: "agent", Policy: config.DefaultServiceConfig().Agent.Policy})
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "node_operation", ActionID: "action", ResourceUID: "node", Generation: 1, LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Minute), Operation: controlplane.NodeOperation{ID: "operation", Topology: "lab", Node: "web", Operation: operation}}
		var events []controlplane.AgentCommandEvent
		runner.Execute(context.Background(), &cmd, func(event controlplane.AgentCommandEvent) { events = append(events, event) })
		require.NotEmpty(t, events)
		require.Equal(t, "failed", events[len(events)-1].Status)
		require.Contains(t, events[len(events)-1].Error, "host supervisor")
	}
	require.Zero(t, calls.Load(), "worker must not manufacture a terminal operation report")
}
