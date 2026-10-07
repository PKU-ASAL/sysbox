//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestHostSupervisorRealFileInstallE2E(t *testing.T) {
	helper := os.Getenv("SYSBOX_GUESTTASK_HELPER")
	if helper == "" {
		t.Skip("requires current guest helper and disposable Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := func() string { p := t.TempDir(); require.NoError(t, os.Chmod(p, 0700)); return p }
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keys := dir()
	keyfile := filepath.Join(keys, "issuer.pub")
	policy := filepath.Join(keys, "roots.json")
	require.NoError(t, os.WriteFile(keyfile, pub, 0600))
	require.NoError(t, os.WriteFile(policy, []byte(`["/var/tmp/sysbox-install"]`), 0600))
	runs, dataDir := dir(), dir()
	api := NewServer(runs, dataDir)
	require.NoError(t, api.ConfigureExecutionGrantSigner(issuer))
	api.grantHostKeys = map[string]ed25519.PublicKey{"owner": hostPub}
	secret := "file-e2e-secret"
	require.NoError(t, api.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	repo, err := api.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	raw, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", "--network", "none", "--label", "sysbox.managed=true", "--label", "sysbox.owner_uid="+st.TopologyUID, "--label", "sysbox.topology_uid="+st.TopologyUID, "--label", "sysbox.resource_uid=node", "--label", "sysbox.generation=1", "-v", helper+":/usr/local/bin/sysbox-guest-task:ro", "-v", keyfile+":/etc/sysbox/grant-public-key:ro", "-v", policy+":/tmp/bootstrap-install-roots:ro", "alpine:3.22", "sh", "-c", "mkdir -m 700 /var/tmp/sysbox-install; cp /tmp/bootstrap-install-roots /etc/sysbox/install-roots.json; chmod 600 /etc/sysbox/install-roots.json; exec sleep 120").CombinedOutput()
	require.NoError(t, err, string(raw))
	container := strings.TrimSpace(string(raw))
	defer func() {
		raw, err := exec.Command("docker", "rm", "-f", container).CombinedOutput()
		require.NoError(t, err, string(raw))
	}()
	provider, err := dockerprovider.New()
	require.NoError(t, err)
	defer provider.Close()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Node: provider, NodeState: provider}))
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: container}}
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
	var apiProcess atomic.Pointer[Server]
	apiProcess.Store(api)
	var drop atomic.Bool
	drop.Store(true)
	dropped := make(chan int, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stopped") && drop.CompareAndSwap(true, false) {
			rr := httptest.NewRecorder()
			apiProcess.Load().ServeHTTP(rr, r)
			c, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				c.Close()
			}
			dropped <- rr.Code
			return
		}
		apiProcess.Load().ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	authority := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "owner", Secret: secret, APIURL: endpoint.URL}}
	resolver := hostsupervisor.CanonicalResolver{RunsDir: runs, Backend: api.stateBackend, AgentID: "owner"}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	root, socketDir := dir(), dir()
	socket := filepath.Join(socketDir, "host.sock")
	start := func() (context.CancelFunc, chan error) {
		task := &hostsupervisor.Dispatcher{Root: root, AgentID: "owner", HostBootID: strings.TrimSpace(string(boot)), IssuerKey: pub, HostKey: hostKey}
		base := *task
		base.Root = filepath.Join(root, "files")
		base.Resolve = resolver.Resolve
		base.Authorize = authority.Authorize
		base.ReportStop = authority.ReportStop
		files := &hostsupervisor.FileDispatcher{Dispatcher: &base, Fetch: authority.FetchFile}
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- (&hostsupervisor.Server{Socket: socket, GroupID: -1, Dispatcher: task, Files: files}).Serve(runCtx)
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
	stop, done := start()
	defer func() { stop(); require.NoError(t, <-done) }()
	ref := hostsupervisor.TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: 1}}
	client := hostsupervisor.NewTaskClient(socket, ref)
	guestBoot, err := client.GuestFileBootID(ctx)
	require.NoError(t, err)
	session, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: api.apiStore}).OpenSession(ctx, "owner", "worker", "")
	require.NoError(t, err)
	var previousEpoch uint64
	for round := 0; round < 2; round++ {
		id := fmt.Sprintf("file-%d", round)
		content := bytes.Repeat([]byte("file-data\n"), 1<<20)
		hash := sha256.Sum256(content)
		put := controlplane.GuestFilePut{ID: id, Topology: "lab", Node: "web", Path: "/var/tmp/sysbox-install/" + id, Mode: 0640, Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:]), FetchRef: "/worker-supplied-ref-is-not-used"}
		op := controlplane.GuestFileOperation{ID: id, Version: 1, AgentID: "owner", CommandID: id, Topology: "lab", Node: "web", Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now().UTC()}
		cmd := controlplane.AgentCommand{ID: id, AgentID: "owner", Type: "guest_file_put", Status: controlplane.AgentCommandStatusQueued, Protocol: controlplane.AgentProtocolVersion, FilePut: &put, TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: 1, LeaseEpoch: 1, ActionID: id}
		require.NoError(t, api.apiStore.CreateGuestFileDispatch(ctx, op, cmd))
		payloadDir := filepath.Join(runs, "_guest-files")
		require.NoError(t, os.MkdirAll(payloadDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(payloadDir, id), content, 0600))
		require.NoError(t, writeLocalObject(filepath.Join(payloadDir, id+".json"), guestFilePayload{AgentID: "owner", ExpiresAt: time.Now().Add(time.Minute)}))
		claimed, ok, err := api.apiStore.AcquireAgentCommandLease(ctx, "owner", id, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = newGuestFileOperationService(api.apiStore).MarkRunning(ctx, id)
		require.NoError(t, err)
		body, _ := json.Marshal(ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: claimed.LeaseOwner, Attempt: claimed.Attempt, BootID: guestBoot})
		r, err := http.NewRequestWithContext(ctx, "POST", endpoint.URL+"/v1/agents/owner/commands/"+id+"/execution-grant", bytes.NewReader(body))
		require.NoError(t, err)
		require.NoError(t, agent.SignRequest(r, "owner", secret, time.Now()))
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)
		var g controlplane.SignedExecutionGrant
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&g))
		resp.Body.Close()
		require.Greater(t, g.Grant.Epoch, previousEpoch)
		previousEpoch = g.Grant.Epoch
		spec, err := controlplane.FromFileInstallCommand(*claimed)
		require.NoError(t, err)
		h := substrate.FileInstallHandle{ID: id, Digest: g.Grant.Digest, BootID: g.BootID, NodeID: ref.Node, TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: 1, Epoch: g.Grant.Epoch}
		if round == 0 {
			_, installErr := client.InstallGuestFile(ctx, spec, g)
			select {
			case code := <-dropped:
				require.Equal(t, 200, code)
			case <-time.After(10 * time.Second):
				t.Fatalf("file stop not reported; install error: %v", installErr)
			}
			stop()
			require.NoError(t, <-done)
			api = NewServer(runs, dataDir)
			require.NoError(t, api.ConfigureExecutionGrantSigner(issuer))
			api.grantHostKeys = map[string]ed25519.PublicKey{"owner": hostPub}
			apiProcess.Store(api)
			stop, done = start()
			result, err := client.ObserveGuestFile(ctx, h)
			require.NoError(t, err)
			require.True(t, result.Quiescent)
			require.Equal(t, substrate.FileInstallCommitted, result.Outcome)
			raw, err := exec.CommandContext(ctx, "docker", "exec", container, "sha256sum", put.Path).CombinedOutput()
			require.NoError(t, err, string(raw))
			require.Equal(t, put.SHA256, strings.Fields(string(raw))[0])
			raw, err = exec.CommandContext(ctx, "docker", "exec", container, "stat", "-c", "%a", put.Path).CombinedOutput()
			require.NoError(t, err, string(raw))
			require.Equal(t, "640", strings.TrimSpace(string(raw)))
		} else {
			// Retire current authority before the delayed installation reaches
			// the host. No guest file may be written by this signed old request.
			require.NoError(t, persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: api.apiStore}).Revoke(ctx, g.Grant))
			_, err = client.InstallGuestFile(ctx, spec, g)
			require.Error(t, err)
			result, err := client.ObserveGuestFile(ctx, h)
			require.NoError(t, err)
			require.Equal(t, substrate.FileInstallCancelled, result.Outcome)
			require.Error(t, exec.CommandContext(ctx, "docker", "exec", container, "test", "-e", put.Path).Run())
		}
		stored, err := api.apiStore.GetGuestFileOperation(ctx, id)
		require.NoError(t, err)
		if round == 0 {
			require.Equal(t, controlplane.GuestExecutionCompleted, stored.Status)
		} else {
			require.Equal(t, controlplane.GuestExecutionCancelled, stored.Status)
		}
		require.Error(t, authority.Authorize(ctx, g))
	}
	t.Log("real 10MiB file installation: trusted payload fetch, checksum/mode, lost host receipt, cold API/supervisor recovery, successor epoch and revoked request rejection verified")
}
