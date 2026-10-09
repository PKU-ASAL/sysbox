package commands

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
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/stretchr/testify/require"
)

func TestSupervisorGCProviderCallbackDoesNotReenterGate(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	candidate := hostsupervisor.GCCandidate{Kind: "container", ExternalID: "orphan", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "references"}
	digest, err := candidate.Digest()
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "gc", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: "boot", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	signed, err := controlplane.SignExecutionGrant(key, grant, "boot", time.Now())
	require.NoError(t, err)
	deleted := false
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("API-Version", "1.41")
		case strings.Contains(r.URL.Path, "/containers/orphan") && r.Method == http.MethodGet:
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "orphan", "Config": map[string]any{"Labels": map[string]string{"sysbox.managed": "true", "sysbox.owner_uid": "owner", "sysbox.topology_uid": "owner", "sysbox.resource_uid": "resource", "sysbox.generation": "1"}}})
		case strings.Contains(r.URL.Path, "/containers/orphan") && r.Method == http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer docker.Close()
	t.Setenv("DOCKER_HOST", docker.URL)
	coordinator := &hostsupervisor.GCCoordinator{Root: t.TempDir(), AgentID: "agent", HostBootID: "boot", IssuerKey: pub, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, ValidateInventory: func(context.Context, hostsupervisor.GCCandidate) error { return nil }}
	effect := newSupervisorGCEffect(config.ServiceConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	socket := filepath.Join(t.TempDir(), "gc.sock")
	require.NoError(t, os.Chmod(filepath.Dir(socket), 0700))
	server := hostsupervisor.Server{Socket: socket, GroupID: -1, GC: coordinator, GCEffect: effect}
	serverCtx, stopServer := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(serverCtx) }()
	t.Cleanup(func() {
		stopServer()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Error("GC server failed to stop")
		}
	})
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("unix", socket, 10*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, time.Second, 10*time.Millisecond)
	client := hostsupervisor.NewTaskClient(socket, hostsupervisor.TargetRef{Topology: "lab", Node: "vm", Scope: grant.Scope})
	raw, err := client.DeleteGC(ctx, candidate, signed)
	require.NoError(t, err, "the provider callback runs inside the existing gate")
	require.JSONEq(t, `{"deleted":true}`, string(raw))
	// The recorded outcome must be observable without repeating the provider call.
	raw, err = client.ObserveGC(ctx, candidate, signed)
	require.NoError(t, err)
	require.JSONEq(t, `{"deleted":true}`, string(raw))
}

func TestSupervisorGCWithoutReferenceValidatorDoesNotWriteIntent(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	candidate := hostsupervisor.GCCandidate{Kind: "container", ExternalID: "external", TopologyUID: "owner", OwnerUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "inventory"}
	digest, err := candidate.Digest()
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "gc", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: "boot", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	signed, err := controlplane.SignExecutionGrant(key, grant, "boot", time.Now())
	require.NoError(t, err)
	root := t.TempDir()
	c := &hostsupervisor.GCCoordinator{Root: root, AgentID: "agent", HostBootID: "boot", IssuerKey: pub, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error {
		t.Fatal("missing reference validator reached authorization")
		return nil
	}}
	_, err = c.Delete(context.Background(), candidate, signed, func(context.Context) (json.RawMessage, error) {
		t.Fatal("missing reference validator permitted effect")
		return nil, nil
	})
	require.Error(t, err)
	files, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, files)
}
