package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestRemoteAdoptionBootstrapsWithoutExistingCanonicalNode(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyPath := filepath.Join(dir, "issuer")
	require.NoError(t, os.WriteFile(keyPath, pub, 0600))
	req := controlplane.AdoptionRequest{ID: "adopt", AgentID: "agent", Topology: "lab", TopologyUID: "topology-uid", Address: "sysbox_node.web", Provider: "docker", ExternalID: "external", ResourceUID: "node", Generation: 1, Evidence: substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "fingerprint"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	grant, err := controlplane.SignExecutionGrant(issuer, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: 1}, TargetBootID: "hostboot", AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: "command", Attempt: 1, Epoch: 1, Operation: "adoption_apply", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, "hostboot", time.Now())
	require.NoError(t, err)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(grant) }))
	defer api.Close()
	var effects atomic.Int32
	server := &hostsupervisor.Server{Socket: filepath.Join(dir, "host.sock"), GroupID: -1, Adoption: &hostsupervisor.AdoptionCoordinator{Root: filepath.Join(dir, "journal"), IssuerKey: pub, HostKey: hostKey, HostBootID: "hostboot", AgentID: "agent", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, Report: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		return nil
	}}, AdoptionApply: func(context.Context, controlplane.AdoptionRequest, controlplane.SignedExecutionGrant) (json.RawMessage, error) {
		effects.Add(1)
		return json.RawMessage(`{"container_name":"web"}`), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	require.Eventually(t, func() bool { _, err := os.Stat(server.Socket); return err == nil }, time.Second, 10*time.Millisecond)
	cmd := controlplane.AgentCommand{ID: "command", Type: "node_operation", AgentID: "agent", TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: 1, ActionID: req.ID, Attempt: 1, LeaseOwner: "worker", Operation: controlplane.NodeOperation{ID: req.ID, Topology: req.Topology, Node: "web", Operation: "adoption_apply", Adoption: &req}}
	err = runGrantedAdoption(ctx, Options{ID: "agent", APIURL: api.URL, ExecutionSessionID: "session", ExecutionGrantPublicKeyFile: keyPath, HostSupervisorSocket: server.Socket}, cmd)
	require.NoError(t, err)
	require.EqualValues(t, 1, effects.Load())
}
