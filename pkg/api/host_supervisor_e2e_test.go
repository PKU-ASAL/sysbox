//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
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
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostSupervisorRealTaskStopAndSuccessorE2E(t *testing.T) {
	helper := os.Getenv("SYSBOX_GUESTTASK_HELPER")
	if helper == "" {
		t.Skip("current guest helper and disposable private-cgroup Docker required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	public, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPublic, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyfile := filepath.Join(t.TempDir(), "issuer.pub")
	require.NoError(t, os.WriteFile(keyfile, public, 0600))
	runsDir, dataDir := t.TempDir(), t.TempDir()
	require.NoError(t, os.Chmod(runsDir, 0700))
	require.NoError(t, os.Chmod(dataDir, 0700))
	server := NewServer(runsDir, dataDir)
	require.NoError(t, server.ConfigureExecutionGrantSigner(issuer))
	server.grantHostKeys = map[string]ed25519.PublicKey{"owner": hostPublic}
	secret := "supervisor-e2e-secret"
	require.NoError(t, server.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	repo, err := server.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	output, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", "--privileged", "--cgroupns=private", "--network", "none",
		"--label", "sysbox.managed=true", "--label", "sysbox.owner_uid="+st.TopologyUID, "--label", "sysbox.topology_uid="+st.TopologyUID, "--label", "sysbox.resource_uid=guest", "--label", "sysbox.generation=1",
		"-v", helper+":/usr/local/bin/sysbox-guest-task:ro", "-v", keyfile+":/etc/sysbox/grant-public-key:ro", "alpine:3.22", "sleep", "120").CombinedOutput()
	require.NoError(t, err, string(output))
	containerID := strings.TrimSpace(string(output))
	defer func() {
		output, err := exec.Command("docker", "rm", "-f", containerID).CombinedOutput()
		require.NoError(t, err, string(output))
	}()
	provider, err := dockerprovider.New()
	require.NoError(t, err)
	defer provider.Close()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Node: provider, NodeState: provider}))
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker", OwnerUID: st.TopologyUID, ResourceUID: "guest", Generation: 1, ExternalID: containerID}}
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
	var lost atomic.Bool
	lost.Store(true)
	lostResponse := make(chan int, 1)
	var apiProcess atomic.Pointer[Server]
	apiProcess.Store(server)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stopped") && lost.CompareAndSwap(true, false) {
			record := httptest.NewRecorder()
			apiProcess.Load().ServeHTTP(record, r)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			lostResponse <- record.Code
			return
		}
		apiProcess.Load().ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	authority := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "owner", Secret: secret, APIURL: endpoint.URL}}
	resolver := hostsupervisor.CanonicalResolver{RunsDir: server.runsDir, Backend: server.stateBackend, AgentID: "owner"}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	root := t.TempDir()
	socket := filepath.Join(t.TempDir(), "host.sock")
	require.NoError(t, os.Chmod(root, 0700))
	require.NoError(t, os.Chmod(filepath.Dir(socket), 0700))
	dispatcher := &hostsupervisor.Dispatcher{Root: root, AgentID: "owner", HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: hostKey, Resolve: resolver.Resolve, Authorize: authority.Authorize, ReportStop: authority.ReportStop}
	dispatcher.PendingStops = authority.PendingGuestTaskStops
	startSupervisor := func() (context.CancelFunc, chan error) {
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- (&hostsupervisor.Server{Socket: socket, GroupID: -1, Dispatcher: dispatcher}).Serve(runCtx)
		}()
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
	stop, done := startSupervisor()
	defer func() { stop(); require.NoError(t, <-done) }()
	ref := hostsupervisor.TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: st.TopologyUID, ResourceUID: "guest", Generation: 1}}
	client := hostsupervisor.NewTaskClient(socket, ref)
	guestBoot, err := client.GuestTaskBootID(ctx)
	require.NoError(t, err)
	session, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: server.apiStore}).OpenSession(ctx, "owner", "first-process", "")
	require.NoError(t, err)
	var previousEpoch uint64
	for round := 0; round < 4; round++ {
		operationID := fmt.Sprintf("operation-%d", round)
		request := controlplane.GuestExecutionRequest{Argv: []string{"/bin/sh", "-c", "printf supervised; exit 7"}}
		if round == 1 {
			request.Argv = []string{"/bin/sh", "-c", "printf supervised; exec sleep 60"}
		}
		operation := controlplane.GuestExecution{ID: operationID, CommandID: operationID, Version: 1, Topology: "lab", Node: "web", AgentID: "owner", Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now().UTC(), Request: request}
		command := controlplane.AgentCommand{ID: operationID, AgentID: "owner", Type: "guest_execution", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, CreatedAt: time.Now().UTC(), TopologyUID: st.TopologyUID, ResourceUID: "guest", Generation: 1, LeaseEpoch: 1, ActionID: operationID, Execution: &operation, ExecutionRequest: request}
		require.NoError(t, server.apiStore.CreateGuestExecutionDispatch(ctx, operation, command))
		claimed, ok, err := server.apiStore.AcquireAgentCommandLease(ctx, "owner", operationID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		body, _ := json.Marshal(ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: claimed.LeaseOwner, Attempt: claimed.Attempt, BootID: guestBoot})
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint.URL+"/v1/agents/owner/commands/"+operationID+"/execution-grant", bytes.NewReader(body))
		require.NoError(t, err)
		require.NoError(t, agent.SignRequest(req, "owner", secret, time.Now()))
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)
		var grant controlplane.SignedExecutionGrant
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&grant))
		resp.Body.Close()
		require.Greater(t, grant.Grant.Epoch, previousEpoch)
		previousEpoch = grant.Grant.Epoch
		task, err := guesttask.FromExecutionCommand(*claimed)
		require.NoError(t, err)
		execRequest := substrate.ExecRequest{Program: task.Args[0], Args: task.Args[1:], Shell: substrate.ShellNone, OutputLimitBytes: task.OutputLimit}
		var handle substrate.ExecutionHandle
		if round == 3 {
			// The grant escaped the API, but no Start request ever reached the
			// host. Only API discovery can create the stop-only recovery record.
			handle = substrate.ExecutionHandle{ID: task.ID, Digest: grant.Grant.Digest, BootID: grant.BootID, Epoch: grant.Grant.Epoch, NodeID: ref.Node, TopologyUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation}
			_, err = server.apiStore.CancelGuestExecution(ctx, operationID)
			require.NoError(t, err)
		} else {
			handle, err = client.Start(transport.WithTaskGrant(ctx, task.ID, grant), execRequest)
			require.NoError(t, err)
		}
		if round == 1 {
			require.Eventually(t, func() bool {
				data, err := client.ReadExecutionOutput(ctx, handle, "stdout", 0, 100)
				return err == nil && string(data.Data) == "supervised"
			}, 5*time.Second, 20*time.Millisecond)
			result, err := client.ObserveExecution(ctx, handle)
			require.NoError(t, err)
			require.Equal(t, substrate.ExecStatusRunning, result.Status)
			// A new Agent incarnation invalidates the live task's authority.
			// No worker cancellation is sent: the supervisor must discover the
			// loss, stop the guest and report independently verified quiescence.
			session, err = persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: server.apiStore}).OpenSession(ctx, "owner", "second-process", session.ID)
			require.NoError(t, err)
			require.Error(t, authority.Authorize(ctx, grant))
		}
		if round == 0 {
			select {
			case code := <-lostResponse:
				require.Equal(t, http.StatusOK, code)
			case <-time.After(5 * time.Second):
				t.Fatal("supervisor did not report confirmed stop")
			}
			stop()
			require.NoError(t, <-done)
			// Recreate both services from durable records, retaining only the
			// operator-provisioned signing keys and configured storage paths.
			server = NewServer(runsDir, dataDir)
			require.NoError(t, server.ConfigureExecutionGrantSigner(issuer))
			server.grantHostKeys = map[string]ed25519.PublicKey{"owner": hostPublic}
			apiProcess.Store(server)
			dispatcher = &hostsupervisor.Dispatcher{Root: root, AgentID: "owner", HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: hostKey, Resolve: resolver.Resolve, Authorize: authority.Authorize, ReportStop: authority.ReportStop}
			dispatcher.PendingStops = authority.PendingGuestTaskStops
			stop, done = startSupervisor()
		}
		var lastResult substrate.ExecResult
		var lastError error
		stopped := assert.Eventually(t, func() bool {
			lastResult, lastError = client.ObserveExecution(ctx, handle)
			return lastError == nil && (round == 1 || round == 3 || lastResult.ExitCodeKnown && lastResult.ExitCode == 7) && lastResult.RemoteTermination == substrate.RemoteTerminationConfirmed
		}, 5*time.Second, 20*time.Millisecond)
		require.True(t, stopped, "round=%d result=%+v error=%v", round, lastResult, lastError)
		data, err := client.ReadExecutionOutput(ctx, handle, "stdout", 0, 100)
		require.NoError(t, err)
		if round == 3 {
			require.Empty(t, data.Data)
		} else {
			require.Equal(t, "supervised", string(data.Data))
		}
		completed, err := server.apiStore.GetGuestExecution(ctx, operationID)
		require.NoError(t, err)
		require.True(t, guestExecutionTerminal(completed.Status), "host report must also finish the API execution")
		if round == 3 {
			require.Equal(t, controlplane.GuestExecutionCancelled, completed.Status)
			require.Empty(t, completed.Result.Stdout)
			target, err := resolver.Resolve(ctx, ref)
			require.NoError(t, err)
			guest := transport.NewTaskClient(target.Connection, target.NodeID, ref.Scope.Generation)
			late, err := guest.Start(transport.WithTaskGrant(ctx, task.ID, grant), execRequest)
			require.NoError(t, err)
			result, err := guest.ObserveExecution(ctx, late)
			require.NoError(t, err)
			require.Equal(t, substrate.RemoteTerminationConfirmed, result.RemoteTermination)
			require.False(t, result.ExitCodeKnown)
			output, err := exec.CommandContext(ctx, "docker", "exec", containerID, "test", "!", "-e", "/sys/fs/cgroup/sysbox-tasks/sysbox-task-"+operationID).CombinedOutput()
			require.NoError(t, err, string(output))
		} else {
			require.Equal(t, "c3VwZXJ2aXNlZA==", completed.Result.Stdout)
		}
		if round != 1 && round != 3 {
			require.Equal(t, controlplane.GuestExecutionCompleted, completed.Status)
			require.Equal(t, 7, completed.Result.ExitCode)
		}
		require.Error(t, authority.Authorize(ctx, grant), "completed epoch must already be retired before successor issuance")
	}
	t.Log("real Docker task: host result replay, restart, session loss, successor fencing, and API-discovered missing host/guest task cancellation with late Start rejection verified")
}
