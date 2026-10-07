//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestHostSupervisorRealNodePowerE2E(t *testing.T) {
	if os.Getenv("SYSBOX_HOST_POWER_E2E") != "1" {
		t.Skip("requires disposable real Docker resource")
	}
	t.Run("running", func(t *testing.T) { testHostNodePower(t, false) })
	t.Run("unhealthy", func(t *testing.T) { testHostNodePower(t, true) })
}
func testHostNodePower(t *testing.T, unhealthy bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	privateDir := func() string { p := t.TempDir(); require.NoError(t, os.Chmod(p, 0700)); return p }
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	runsDir, dataDir := privateDir(), privateDir()
	api := NewServer(runsDir, dataDir)
	require.NoError(t, api.ConfigureExecutionGrantSigner(issuer))
	api.grantHostKeys = map[string]ed25519.PublicKey{"owner": hostPub}
	secret := "power-e2e-secret"
	require.NoError(t, api.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	repo, err := api.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	args := []string{"run", "-d", "--rm", "--network", "none", "--label", "sysbox.managed=true", "--label", "sysbox.owner_uid=" + st.TopologyUID, "--label", "sysbox.topology_uid=" + st.TopologyUID, "--label", "sysbox.resource_uid=node", "--label", "sysbox.generation=1"}
	if unhealthy {
		args = append(args, "--health-cmd", "exit 1", "--health-interval", "100ms", "--health-retries", "1")
	}
	args = append(args, "alpine:3.22", "sleep", "120")
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, string(out))
	containerID := strings.TrimSpace(string(out))
	defer func() {
		out, err := exec.Command("docker", "rm", "-f", containerID).CombinedOutput()
		require.NoError(t, err, string(out))
	}()
	if unhealthy {
		require.Eventually(t, func() bool {
			raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Health.Status}}", containerID).CombinedOutput()
			return err == nil && strings.TrimSpace(string(raw)) == "unhealthy"
		}, 5*time.Second, 50*time.Millisecond)
	}
	provider, err := dockerprovider.New()
	require.NoError(t, err)
	defer provider.Close()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Node: provider, NodeState: provider, Power: provider}))
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: containerID}}
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
	var currentAPI atomic.Pointer[Server]
	currentAPI.Store(api)
	var drop atomic.Bool
	drop.Store(true)
	dropped := make(chan int, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stopped") && drop.CompareAndSwap(true, false) {
			recorded := httptest.NewRecorder()
			currentAPI.Load().ServeHTTP(recorded, r)
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				c.Close()
			}
			dropped <- recorded.Code
			return
		}
		currentAPI.Load().ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	authority := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "owner", Secret: secret, APIURL: endpoint.URL}}
	resolver := hostsupervisor.CanonicalResolver{RunsDir: runsDir, Backend: api.stateBackend, AgentID: "owner"}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	root, socketDir := privateDir(), privateDir()
	socket := filepath.Join(socketDir, "host.sock")
	start := func() (context.CancelFunc, chan error) {
		power := &hostsupervisor.PowerDispatcher{Root: filepath.Join(root, "power"), AgentID: "owner", HostBootID: strings.TrimSpace(string(boot)), IssuerKey: pub, HostKey: hostKey, Resolve: resolver.ResolveNode, Authorize: authority.Authorize, ReportStop: authority.ReportStop}
		tasks := &hostsupervisor.Dispatcher{Root: root, AgentID: "owner", HostBootID: strings.TrimSpace(string(boot)), IssuerKey: pub, HostKey: hostKey}
		srv := &hostsupervisor.Server{Socket: socket, GroupID: -1, Dispatcher: tasks, Power: power}
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- srv.Serve(runCtx) }()
		require.Eventually(t, func() bool {
			c, e := net.DialTimeout("unix", socket, 10*time.Millisecond)
			if e == nil {
				c.Close()
				return true
			}
			return false
		}, time.Second, 10*time.Millisecond)
		return stop, done
	}
	stop, done := start()
	defer func() { stop(); require.NoError(t, <-done) }()
	ref := hostsupervisor.TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: 1}}
	client := hostsupervisor.NewTaskClient(socket, ref)
	hostBoot, err := client.HostBootID(ctx)
	require.NoError(t, err)
	session, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: api.apiStore}).OpenSession(ctx, "owner", "worker-one", "")
	require.NoError(t, err)
	var pauseGrant controlplane.SignedExecutionGrant
	for _, action := range []string{"pause", "resume"} {
		service := &NodeOperationService{operations: newNodeOperationStore(api.apiStore)}
		op, err := service.enqueue(ctx, st.TopologyUID, prepareNodeOperation(controlplane.NodeOperation{Topology: "lab", Node: "web", AgentID: "owner", Operation: action, ResourceUID: "node", Generation: 1}))
		require.NoError(t, err)
		command, ok, err := api.apiStore.AcquireAgentCommandLease(ctx, "owner", op.CommandID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		body, err := json.Marshal(ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: command.LeaseOwner, Attempt: command.Attempt, BootID: hostBoot})
		require.NoError(t, err)
		r, err := http.NewRequestWithContext(ctx, "POST", endpoint.URL+"/v1/agents/owner/commands/"+command.ID+"/execution-grant", bytes.NewReader(body))
		require.NoError(t, err)
		require.NoError(t, agent.SignRequest(r, "owner", secret, time.Now()))
		response, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		require.Equal(t, 200, response.StatusCode)
		var grant controlplane.SignedExecutionGrant
		require.NoError(t, json.NewDecoder(response.Body).Decode(&grant))
		response.Body.Close()
		request, err := controlplane.FromNodeOperationCommand(*command)
		require.NoError(t, err)
		wrong := ref
		wrong.Scope.Generation++
		require.Error(t, hostsupervisor.NewTaskClient(socket, wrong).ExecutePower(ctx, request, grant))
		if action == "pause" {
			pauseGrant = grant
			// The first stop is committed by the API but its response is lost.
			_ = client.ExecutePower(ctx, request, grant)
			select {
			case code := <-dropped:
				require.Equal(t, 200, code)
			case <-time.After(5 * time.Second):
				t.Fatal("stop receipt not reported")
			}
			stop()
			require.NoError(t, <-done)
			api = NewServer(runsDir, dataDir)
			require.NoError(t, api.ConfigureExecutionGrantSigner(issuer))
			api.grantHostKeys = map[string]ed25519.PublicKey{"owner": hostPub}
			currentAPI.Store(api)
			stop, done = start()
			require.NoError(t, client.ObservePower(ctx, grant))
		} else {
			require.Greater(t, grant.Grant.Epoch, pauseGrant.Grant.Epoch)
			require.NoError(t, client.ExecutePower(ctx, request, grant))
			// Historical evidence is readable without reapplying old pause.
			require.NoError(t, client.ObservePower(ctx, pauseGrant))
			require.Error(t, client.ExecutePower(ctx, controlplane.NodePowerRequest{Topology: "lab", Node: "web", Operation: "pause"}, pauseGrant))
		}
		out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Paused}}", containerID).CombinedOutput()
		require.NoError(t, err, string(out))
		require.Equal(t, action == "pause", strings.TrimSpace(string(out)) == "true")
		require.Error(t, authority.Authorize(ctx, grant))
	}
	t.Log("real pause/resume: canonical resource fencing, persisted stop, lost acknowledgment, API/supervisor reconstruction, successor epoch and historical read without replay verified")
}
