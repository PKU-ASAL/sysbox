//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// Starts with public HTTP admission; the real Agent leases commands and the
// production issuer signs them. No command, mutation grant or receipt is seeded.
func TestPublicSSHAgentDockerE2E(t *testing.T) {
	helper := os.Getenv("SYSBOX_GUESTTASK_HELPER")
	if helper == "" {
		t.Skip("requires current static guest helper and disposable Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := func() string { p := t.TempDir(); require.NoError(t, os.Chmod(p, 0700)); return p }
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyfile := filepath.Join(dir(), "issuer.pub")
	require.NoError(t, os.WriteFile(keyfile, pub, 0600))
	runs, data := dir(), dir()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir = runs, data
	cfg.State.Backend = "sqlite://" + filepath.Join(dir(), "canonical.db")
	api := NewServerWithConfig(cfg)
	require.NoError(t, api.initErr)
	require.NoError(t, api.ConfigureExecutionGrantSigner(issuer))
	api.grantHostKeys = map[string]ed25519.PublicKey{"owner": hostPub}
	secret := "ssh-public-test-secret"
	require.NoError(t, api.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", AuthSecret: secret, SecretHash: agent.SecretHash(secret), Status: "online", Protocol: controlplane.AgentProtocolVersion, Capabilities: []string{"docker"}}))
	repo, err := api.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	raw, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", "--network", "none", "--label", "sysbox.managed=true", "--label", "sysbox.owner_uid="+st.TopologyUID, "--label", "sysbox.topology_uid="+st.TopologyUID, "--label", "sysbox.resource_uid=node", "--label", "sysbox.generation=1", "-v", helper+":/usr/local/bin/sysbox-guest-task:ro", "-v", keyfile+":/etc/sysbox/grant-public-key:ro", "alpine:3.22", "sleep", "120").CombinedOutput()
	require.NoError(t, err, string(raw))
	container := strings.TrimSpace(string(raw))
	defer func() {
		out, e := exec.Command("docker", "rm", "-f", container).CombinedOutput()
		require.NoError(t, e, string(out))
	}()
	provider, err := dockerprovider.New()
	require.NoError(t, err)
	defer provider.Close()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Node: provider, NodeState: provider}))
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: container, Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []map[string]any{{"User": "root"}}}})}}
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
	var loseAck atomic.Bool
	loseAck.Store(true)
	var reports atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/hosts/owner/node-operations/") && strings.HasSuffix(r.URL.Path, "/completed") {
			reports.Add(1)
			if loseAck.CompareAndSwap(true, false) {
				recorder := httptest.NewRecorder()
				api.ServeHTTP(recorder, r)
				if recorder.Code != 200 {
					t.Errorf("first host completion: %d %s", recorder.Code, recorder.Body.String())
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
		}
		api.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	authority := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "owner", Secret: secret, APIURL: endpoint.URL}}
	resolver := hostsupervisor.CanonicalResolver{RunsDir: runs, Backend: api.stateBackend, AgentID: "owner"}
	root := dir()
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	tasks := &hostsupervisor.Dispatcher{Root: filepath.Join(root, "tasks"), AgentID: "owner", HostBootID: strings.TrimSpace(string(boot)), IssuerKey: pub, HostKey: hostKey, Resolve: resolver.Resolve, Authorize: authority.Authorize}
	sshBase := *tasks
	sshBase.Root = filepath.Join(root, "guest-ssh")
	host := &hostsupervisor.Server{Socket: filepath.Join(root, "host.sock"), GroupID: -1, Dispatcher: tasks, SSHGuest: &hostsupervisor.SSHGuestDispatcher{Dispatcher: &sshBase, Report: authority.ReportNodeOperation}}
	observerConfig := cfg
	observerConfig.Paths.RunsDir, observerConfig.Paths.WorkspacesDir = runs, data
	hostPublicFile := wireTestHostObserver(t, host, observerConfig, "owner", []string{"docker"}, hostKey)
	require.NoError(t, os.MkdirAll(filepath.Join(data, "lab"), 0700))
	require.NoError(t, api.apiStore.(durablePlacementStore).SaveTopologyPlacement(ctx, topologyPlacement{Topology: "lab", AgentID: "owner", Protocol: controlplane.AgentProtocolVersion, BoundAt: time.Now()}))
	hostCtx, stopHost := context.WithCancel(ctx)
	hostDone := make(chan error, 1)
	go func() { hostDone <- host.Serve(hostCtx) }()
	defer func() { stopHost(); require.NoError(t, <-hostDone) }()
	require.Eventually(t, func() bool {
		c, e := net.DialTimeout("unix", host.Socket, 10*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	}, time.Second, 10*time.Millisecond)
	opts := agentexec.Options{ID: "owner", Secret: secret, APIURL: endpoint.URL, Capabilities: []string{"docker"}, HostSupervisorSocket: host.Socket, ExecutionGrantPublicKeyFile: keyfile, ExecutionSessionFile: filepath.Join(dir(), "session.json"), PollInterval: 50 * time.Millisecond, Policy: config.DefaultServiceConfig().Agent.Policy}
	opts.HostPublicKeyFile = hostPublicFile
	agentCtx, stopAgent := context.WithCancel(ctx)
	agentDone := make(chan error, 1)
	go func() { agentDone <- agentexec.Run(agentCtx, opts) }()
	defer func() {
		stopAgent()
		select {
		case <-agentDone:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop")
		}
	}()
	require.Eventually(t, func() bool {
		inventory, e := api.apiStore.GetAgentInventory(ctx, "owner")
		return e == nil && len(inventory.Topologies) > 0
	}, 5*time.Second, 20*time.Millisecond)
	key, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	for _, step := range []struct {
		id, operation string
		revision      uint64
		present       bool
	}{{"create", "ssh_access_apply", 0, true}, {"rotate", "ssh_access_apply", 1, true}, {"revoke", "ssh_access_delete", 2, false}} {
		t.Logf("public SSH step %s", step.id)
		body := map[string]any{"id": step.id, "operation": step.operation, "node": "web", "access_id": "admin", "expected_revision": step.revision}
		if step.present {
			if step.revision > 0 {
				rotationKey, _, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
				key, err = ssh.NewPublicKey(rotationKey)
				require.NoError(t, err)
			}
			body["authorized_keys"] = []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
		}
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(ctx, "POST", endpoint.URL+"/v1/topologies/lab/ssh-access", bytes.NewReader(raw))
		require.NoError(t, err)
		request.Header.Set("X-Sysbox-Roles", "admin")
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		var accepted controlplane.NodeOperation
		responseBody, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, 202, response.StatusCode, string(responseBody))
		require.NoError(t, json.Unmarshal(responseBody, &accepted))
		var completed controlplane.NodeOperation
		require.Eventually(t, func() bool {
			op, e := api.apiStore.GetNodeOperation(ctx, step.id)
			if e != nil {
				return false
			}
			completed = *op
			return op.Status == controlplane.NodeOperationStatusDone
		}, 15*time.Second, 30*time.Millisecond, "operation failed to converge: %s", step.id)
		require.NotNil(t, completed.SSHResult)
		require.Equal(t, step.revision+1, completed.SSHResult.Handle.Revision)
		require.Equal(t, step.present, completed.SSHResult.Present)
		ref := hostsupervisor.TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: 1}}
		found, err := hostsupervisor.NewTaskClient(host.Socket, ref).DiscoverManagedSSHAccess(ctx, "admin")
		require.NoError(t, err)
		require.Equal(t, completed.SSHResult, found.Current)
		if step.present {
			require.Equal(t, body["authorized_keys"], found.Config.AuthorizedKeys)
		}
	}
	require.Eventually(t, func() bool { return reports.Load() >= 4 }, 5*time.Second, 20*time.Millisecond, "lost acknowledgement must cause an identical receipt to be reported again")
}
