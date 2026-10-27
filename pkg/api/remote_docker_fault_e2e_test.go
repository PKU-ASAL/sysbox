//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// Production Agent HTTP/WS, host-supervisor Unix RPC and real Docker share
// canonical SQLite but use separate revision directories. Reconstruction is
// in-process; independent process/VM faults have separate acceptance gates.
func TestRemoteDockerCompletionLossRecoveryE2E(t *testing.T) {
	testRemoteDockerCompletionLossRecovery(t, false, false)
}

func TestRemoteDockerReturnedDestroyRecoveryE2E(t *testing.T) {
	testRemoteDockerCompletionLossRecovery(t, false, false, true)
}

func TestRemoteDockerColdReturnedDestroyRecoveryE2E(t *testing.T) {
	testRemoteDockerCompletionLossRecovery(t, false, false, true, true)
}

func TestRemoteDockerColdResetContinuationE2E(t *testing.T) {
	testRemoteDockerCompletionLossRecovery(t, false, true, false, false, true)
}

func TestRemoteDockerRepeatedResetContinuationE2E(t *testing.T) {
	testRemoteDockerCompletionLossRecovery(t, false, true, false, false, true, true)
}

type failDeletePublication struct {
	runtime.ActionRepository
	fail           *atomic.Bool
	returned       chan struct{}
	resetPrepared  chan struct{}
	resetDestroyed chan struct{}
	secondFault    *atomic.Bool
}

func (r failDeletePublication) ProveCleanup(ctx context.Context, run controlplane.Run) (*controlplane.CleanupReceipt, error) {
	return r.ActionRepository.(runtime.CleanupProver).ProveCleanup(ctx, run)
}

func (r failDeletePublication) CommitResetIntents(ctx context.Context, actions []runtime.ActionRecord) error {
	return r.ActionRepository.(runtime.ResetIntentCommitter).CommitResetIntents(ctx, actions)
}

func (r failDeletePublication) CommitResetContinuation(ctx context.Context, request runtime.ResetContinuationRequest) (*runtime.ResetContinuationReceipt, error) {
	return r.ActionRepository.(runtime.ResetContinuationCommitter).CommitResetContinuation(ctx, request)
}

func (r failDeletePublication) LoadResetContinuation(ctx context.Context, topology, run string) (*runtime.ResetContinuationReceipt, error) {
	return r.ActionRepository.(runtime.ResetContinuationCommitter).LoadResetContinuation(ctx, topology, run)
}

type countedDockerDeletion struct {
	*dockerprovider.Substrate
	calls        atomic.Int32
	preparations atomic.Int32
	resetDeletes atomic.Int32
	replacements atomic.Int32
}

func (d *countedDockerDeletion) PrepareReset(ctx context.Context, request substrate.ResetRequest) (substrate.ResetHandle, error) {
	d.preparations.Add(1)
	return d.Substrate.PrepareReset(ctx, request)
}

func (d *countedDockerDeletion) DestroyReset(ctx context.Context, handle substrate.ResetHandle) error {
	d.resetDeletes.Add(1)
	return d.Substrate.DestroyReset(ctx, handle)
}

func (d *countedDockerDeletion) ApplyReset(ctx context.Context, handle substrate.ResetHandle) (substrate.NodeHandle, error) {
	d.replacements.Add(1)
	return d.Substrate.ApplyReset(ctx, handle)
}

func (d *countedDockerDeletion) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	d.calls.Add(1)
	return d.Substrate.DestroyNode(ctx, h)
}

func (r failDeletePublication) CommitActionOutcome(ctx context.Context, a runtime.ActionRecord, patch *runtime.StatePatch) error {
	if r.resetDestroyed != nil && patch == nil && a.Action == controlplane.PlanActionReset && a.Phase == "destroyed" && r.secondFault.CompareAndSwap(true, false) {
		if err := r.ActionRepository.CommitActionOutcome(ctx, a, nil); err != nil {
			return err
		}
		close(r.resetDestroyed)
		<-ctx.Done()
		return ctx.Err()
	}
	if r.resetPrepared != nil && patch == nil && a.Action == controlplane.PlanActionReset && a.Phase == "prepared" && r.fail.CompareAndSwap(true, false) {
		if err := r.ActionRepository.CommitActionOutcome(ctx, a, nil); err != nil {
			return err
		}
		close(r.resetPrepared)
		<-ctx.Done()
		return ctx.Err()
	}
	if patch != nil && a.Action == controlplane.PlanActionDelete && r.fail.CompareAndSwap(true, false) {
		if r.returned != nil {
			close(r.returned)
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("injected deletion publication unavailable after provider return")
	}
	return r.ActionRepository.CommitActionOutcome(ctx, a, patch)
}

type destroyFailureHostBridge struct {
	*HostExecutionBridge
	fail           *atomic.Bool
	returned       chan struct{}
	resetPrepared  chan struct{}
	resetDestroyed chan struct{}
	secondFault    *atomic.Bool
}

func (b destroyFailureHostBridge) ActionRepository(topology string) (runtime.ActionRepository, error) {
	r, err := b.HostExecutionBridge.ActionRepository(topology)
	return failDeletePublication{ActionRepository: r, fail: b.fail, returned: b.returned, resetPrepared: b.resetPrepared, resetDestroyed: b.resetDestroyed, secondFault: b.secondFault}, err
}

func TestRemoteDockerSSHAccessRunE2E(t *testing.T) {
	if os.Getenv("SYSBOX_GUESTTASK_HELPER") == "" {
		t.Skip("current guest helper required")
	}
	testRemoteDockerCompletionLossRecovery(t, true, false)
}

func TestRemoteDockerTargetedResetRunE2E(t *testing.T) {
	testRemoteDockerCompletionLossRecovery(t, false, true)
}

func TestRemoteDockerTargetedResetSSHRunE2E(t *testing.T) {
	if os.Getenv("SYSBOX_GUESTTASK_HELPER") == "" {
		t.Skip("current guest helper required")
	}
	testRemoteDockerCompletionLossRecovery(t, true, true)
}

func testRemoteDockerCompletionLossRecovery(t *testing.T, managedSSH, targetedReset bool, destroyPublicationFault ...bool) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires real Docker daemon; set SYSBOX_REMOTE_DOCKER_E2E=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	privateDir := func() string { p := t.TempDir(); require.NoError(t, os.Chmod(p, 0700)); return p }
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyPath := filepath.Join(privateDir(), "issuer.pub")
	require.NoError(t, os.WriteFile(keyPath, pub, 0600))
	docker, err := dockerprovider.New()
	require.NoError(t, err)
	defer docker.Close()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	defer cli.Close()
	_, err = cli.Ping(ctx)
	require.NoError(t, err)
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	deletions := &countedDockerDeletion{Substrate: docker}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "1", Node: deletions, NIC: docker, NodeState: docker, Artifact: docker, ImageEntry: docker, GuestExec: docker, GuestNetwork: docker, Reset: deletions}))
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir = privateDir(), privateDir()
	cfg.State.Backend = "sqlite://" + filepath.Join(privateDir(), "canonical.db")
	newAPI := func() *Server {
		s := NewServerWithConfig(cfg)
		require.NoError(t, s.initErr)
		require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
		s.grantHostKeys = map[string]ed25519.PublicKey{"fault-agent": hostPub}
		return s
	}
	first := newAPI()
	agentCfg := cfg
	agentCfg.Paths.RunsDir = t.TempDir()
	agentCfg.Paths.WorkspacesDir = t.TempDir()
	// Remote observation must not depend on worker access to canonical state.
	agentCfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "worker-local.db")
	bridge := NewExecutionBridge(agentCfg)
	topology := "remote-e2e-" + uuid.NewString()[:8]
	repository, err := first.actionRepository(topology)
	require.NoError(t, err)
	initial, err := repository.LoadState(ctx, topology)
	require.NoError(t, err)
	owned := filters.NewArgs(filters.Arg("label", "sysbox.owner_uid="+initial.TopologyUID))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		instances, listErr := cli.ContainerList(cleanup, container.ListOptions{All: true, Filters: owned})
		require.NoError(t, listErr)
		for _, instance := range instances {
			require.Equal(t, initial.TopologyUID, instance.Labels["sysbox.owner_uid"])
			require.NoError(t, cli.ContainerRemove(cleanup, instance.ID, container.RemoveOptions{Force: true}))
		}
	}()
	var current atomic.Pointer[Server]
	current.Store(first)
	type observedCompletion struct {
		controlplane.RunCompletion
		Proof hostRunCompletion
	}
	lost := make(chan observedCompletion, 1)
	acknowledged := make(chan hostRunCompletion, 16)
	var drop atomic.Bool
	var hold atomic.Bool
	drop.Store(true)
	hold.Store(true)
	var faultOperation atomic.Value
	faultOperation.Store("run_apply")
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/hosts/") || !strings.Contains(r.URL.Path, "/runs/") || !strings.HasSuffix(r.URL.Path, "/completed") {
			current.Load().ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", 500)
			return
		}
		var completion observedCompletion
		if json.Unmarshal(raw, &completion.Proof) != nil || json.Unmarshal(completion.Proof.Evidence, &completion.Run) != nil {
			http.Error(w, "decode failed", 500)
			return
		}
		completion.Cleanup = completion.Run.Cleanup
		r.Body = io.NopCloser(bytes.NewReader(raw))
		record := httptest.NewRecorder()
		current.Load().ServeHTTP(record, r)
		if record.Code == 200 {
			// Bind the fault to this phase; a delayed Apply retry must not
			// consume Destroy's fault injection.
			if hold.Load() && completion.Proof.Receipt.Grant.Operation == faultOperation.Load().(string) {
				if drop.CompareAndSwap(true, false) {
					lost <- completion
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
						return
					}
				}
				http.Error(w, "committed; acknowledgement withheld", 503)
				return
			}
			select {
			case acknowledged <- completion.Proof:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(record.Code)
		_, _ = w.Write(record.Body.Bytes())
	}))
	defer host.Close()
	hostCfg := cfg
	hostCfg.Paths.RunsDir, hostCfg.Paths.WorkspacesDir = privateDir(), privateDir()
	authority := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "fault-agent", Secret: "test-agent-secret", APIURL: host.URL}}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	root, socketDir := privateDir(), privateDir()
	var failPublication atomic.Bool
	var returned chan struct{}
	var resetPrepared chan struct{}
	var resetDestroyed chan struct{}
	var secondFault atomic.Bool
	if len(destroyPublicationFault) > 3 && destroyPublicationFault[3] {
		resetDestroyed = make(chan struct{})
		secondFault.Store(true)
	}
	if len(destroyPublicationFault) > 2 && destroyPublicationFault[2] {
		resetPrepared = make(chan struct{})
	}
	if len(destroyPublicationFault) > 1 && destroyPublicationFault[1] {
		returned = make(chan struct{})
	}
	hostErrors := make(chan error, 1)
	socket := filepath.Join(socketDir, "host.sock")
	startHost := func() (context.CancelFunc, <-chan error) {
		bridge, err := NewHostExecutionBridge(hostCfg)
		require.NoError(t, err)
		require.NoError(t, bridge.BindHostIdentity("fault-agent", hostPub))
		resolver := hostsupervisor.CanonicalResolver{RunsDir: hostCfg.Paths.RunsDir, Backend: cfg.State.Backend, AgentID: "fault-agent"}
		checkedAuthorize := func(ctx context.Context, g controlplane.SignedExecutionGrant) error {
			start := time.Now()
			err := authority.Authorize(ctx, g)
			if err != nil {
				t.Logf("authorize run=%s action=%s elapsed=%s ctx=%v error=%v", g.Grant.CommandID, g.Grant.ActionID, time.Since(start), ctx.Err(), err)
			}
			return err
		}
		tasks := &hostsupervisor.Dispatcher{Root: root, AgentID: "fault-agent", HostBootID: strings.TrimSpace(string(boot)), IssuerKey: pub, HostKey: hostKey, Authorize: checkedAuthorize, ReportStop: authority.ReportStop, Resolve: resolver.Resolve, ResolveRun: resolver.ResolveRunTask}
		runs := &hostsupervisor.RunCoordinator{Root: filepath.Join(root, "runs"), AgentID: tasks.AgentID, HostBootID: tasks.HostBootID, IssuerKey: pub, HostKey: hostKey, Authorize: checkedAuthorize, ReportRunID: authority.ReportRun, Tasks: tasks,
			OnExecutionError: func(req hostsupervisor.RunRequest, err error) {
				t.Logf("host %s: %v", req.RunID, err)
				select {
				case hostErrors <- err:
				default:
				}
			}}
		executor := &agentexec.HostRunExecutor{Bridge: bridge, Tasks: tasks, Resolver: resolver, Authority: authority}
		if len(destroyPublicationFault) != 0 && destroyPublicationFault[0] || resetPrepared != nil {
			executor.Bridge = destroyFailureHostBridge{HostExecutionBridge: bridge, fail: &failPublication, returned: returned, resetPrepared: resetPrepared, resetDestroyed: resetDestroyed, secondFault: &secondFault}
		}
		runs.PrepareRun = executor.PrepareRun
		runs.RecoverInterruptedRun = executor.RecoverInterruptedRun
		srv := &hostsupervisor.Server{Socket: socket, GroupID: -1, Dispatcher: tasks, Runs: runs, RunEffect: executor.Execute, RunRecovery: executor.Recover}
		sshBase := *tasks
		sshBase.Root = filepath.Join(root, "guest-ssh")
		srv.SSHGuest = &hostsupervisor.SSHGuestDispatcher{Dispatcher: &sshBase, Report: authority.ReportNodeOperation, ReportDerived: authority.ReportDerivedSSHAccess}
		executor.SSH = srv.SSHGuest
		service, stop := context.WithCancel(ctx)
		t.Cleanup(stop)
		done := make(chan error, 1)
		go func() { defer close(done); done <- srv.Serve(service) }()
		require.Eventually(t, func() bool {
			conn, err := net.DialTimeout("unix", socket, 10*time.Millisecond)
			if err != nil {
				return false
			}
			conn.Close()
			return true
		}, time.Second, 10*time.Millisecond)
		return stop, done
	}
	stopHost, hostDone := startHost()
	defer func() { stopHost(); require.NoError(t, <-hostDone) }()
	awaitReceipt := func(want hostRunCompletion) {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case got := <-acknowledged:
				if got.Receipt.Grant.CommandID != want.Receipt.Grant.CommandID {
					continue
				}
				require.Equal(t, want.Receipt, got.Receipt)
				require.JSONEq(t, string(want.Evidence), string(got.Evidence))
				return
			case <-timer.C:
				t.Fatal("original receipt not acknowledged after reconstruction")
			}
		}
	}
	agentCtx, stopAgent := context.WithCancel(ctx)
	agentDone := make(chan error, 1)
	opts := agentexec.Options{ExecutionSessionFile: filepath.Join(t.TempDir(), "session.json"), APIURL: host.URL, ID: "fault-agent", Secret: "test-agent-secret", Capabilities: []string{"docker", "veth"}, PollInterval: 20 * time.Millisecond, Policy: cfg.Agent.Policy}
	opts.HostSupervisorSocket, opts.RequireHostSupervisor, opts.ExecutionGrantPublicKeyFile = socket, true, keyPath
	go func() { agentDone <- agentexec.Run(agentCtx, opts, bridge) }()
	defer func() {
		stopAgent()
		select {
		case <-agentDone:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop")
		}
	}()
	require.Eventually(t, func() bool { _, err := first.apiStore.GetAgent(ctx, opts.ID); return err == nil }, 5*time.Second, 20*time.Millisecond)
	request := func(method, path string, body any, key string) (int, []byte) {
		raw, marshalErr := json.Marshal(body)
		require.NoError(t, marshalErr)
		req, newErr := http.NewRequestWithContext(ctx, method, host.URL+path, bytes.NewReader(raw))
		require.NoError(t, newErr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Sysbox-Roles", "admin")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if strings.HasPrefix(path, "/v1/agents/") {
			require.NoError(t, agent.SignRequest(req, opts.ID, opts.Secret, time.Now()))
		}
		response, callErr := http.DefaultClient.Do(req)
		require.NoError(t, callErr)
		defer response.Body.Close()
		data, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		return response.StatusCode, data
	}
	hcl := fmt.Sprintf(`resource "sysbox_image" "base" {
  substrate = "docker"
  source = "alpine:3.22"
  kind = "oci"
  architecture = "amd64"
  guest_family = "linux"
}
resource "sysbox_node" "%s" {
  substrate = "docker"
  image = sysbox_image.base.id
  provider "docker" {
    entrypoint = ["sleep"]
    command = ["infinity"]
  }
}
`, strings.ReplaceAll(topology, "-", "_"))
	if managedSSH {
		helper := os.Getenv("SYSBOX_GUESTTASK_HELPER")
		key, err := ssh.NewPublicKey(pub)
		require.NoError(t, err)
		hcl = strings.Replace(hcl, "  provider \"docker\" {", fmt.Sprintf("  connection {\n    type = \"ssh\"\n    user = \"root\"\n  }\n  provider \"docker\" {\n    binds = [%q, %q]", helper+":/usr/local/bin/sysbox-guest-task:ro", keyPath+":/etc/sysbox/grant-public-key:ro"), 1)
		hcl += fmt.Sprintf("resource \"sysbox_ssh_access\" \"admin\" {\n  node = sysbox_node.%s.id\n  authorized_keys = [%q]\n}\n", strings.ReplaceAll(topology, "-", "_"), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	}
	status, raw := request("POST", "/v1/revisions", map[string]any{"files": map[string]string{"field.sysbox.hcl": hcl}}, "")
	require.Equal(t, 201, status, string(raw))
	var revision map[string]string
	require.NoError(t, json.Unmarshal(raw, &revision))
	applyBody := map[string]any{"revision": revision["revision"]}
	status, raw = request("POST", "/v1/topologies/"+topology+"/apply", applyBody, "request-one")
	require.Equal(t, 202, status, string(raw))
	var accepted map[string]any
	require.NoError(t, json.Unmarshal(raw, &accepted))
	runID := accepted["run_id"].(string)
	var completion observedCompletion
	select {
	case completion = <-lost:
	case err := <-hostErrors:
		t.Fatalf("host Run failed before completion: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for signed host completion")
	}
	require.Equal(t, controlplane.RunDone, completion.Run.Status, completion.Run.Err)
	require.NoError(t, controlplane.VerifyRunStopReceipt(hostPub, completion.Proof.Receipt, completion.Proof.Evidence))
	hostBridge, err := NewHostExecutionBridge(hostCfg)
	require.NoError(t, err)
	localRevision, err := hostBridge.MaterializeRunRevision(ctx, topology, completion.Run.Revision)
	require.NoError(t, err)
	require.NotEqual(t, completion.Run.SnapshotPath, localRevision, "execution must materialize the revision in the host workspace")
	state, err := repository.LoadState(ctx, topology)
	require.NoError(t, err)
	var externalID string
	for _, resource := range state.Resources {
		if resource.Address.Type == "sysbox_node" {
			externalID = resource.ExternalID
			require.EqualValues(t, 1, resource.Generation)
		}
	}
	require.NotEmpty(t, externalID)
	if managedSSH {
		var accessFound bool
		for _, resource := range state.Resources {
			if resource.Address.Type == "sysbox_ssh_access" {
				accessFound = true
				require.Equal(t, 2, resource.SchemaVersion)
				require.NotNil(t, resource.Attributes["ssh_access"])
			}
		}
		require.True(t, accessFound, "Run must commit the versioned SSH resource")
	}
	inspected, err := cli.ContainerInspect(ctx, externalID)
	require.NoError(t, err)
	require.True(t, inspected.State.Running)
	stopAgent()
	select {
	case <-agentDone:
	case <-time.After(5 * time.Second):
		t.Fatal("original agent did not stop")
	}
	stopHost()
	require.NoError(t, <-hostDone)
	restarted := newAPI()
	current.Store(restarted)
	stopHost, hostDone = startHost()
	agentCtx, stopAgent = context.WithCancel(ctx)
	agentDone = make(chan error, 1)
	reopenedBridge := NewExecutionBridge(agentCfg)
	go func() { agentDone <- agentexec.Run(agentCtx, opts, reopenedBridge) }()
	hold.Store(false)
	awaitReceipt(completion.Proof)
	status, raw = request("POST", "/v1/topologies/"+topology+"/apply", applyBody, "request-one")
	require.Equal(t, 202, status, string(raw))
	var replay map[string]any
	require.NoError(t, json.Unmarshal(raw, &replay))
	require.Equal(t, runID, replay["run_id"])
	stored, err := restarted.apiStore.GetRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunDone, stored.Status)
	instances, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: owned})
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, externalID, instances[0].ID)
	status, raw = request("GET", "/v1/topologies/"+topology, nil, "")
	require.Equal(t, 200, status, string(raw))
	var workspace WorkspaceInfo
	require.NoError(t, json.Unmarshal(raw, &workspace))
	require.True(t, workspace.HasState, "host and API share canonical repository")
	require.Equal(t, opts.ID, workspace.PlacementAgentID)
	status, raw = request("POST", "/v1/agents/"+opts.ID+"/runs/"+runID+"/claim", controlplane.RunClaim{QueuedAt: stored.QueuedAt.Add(-time.Second), Revision: stored.Revision}, "")
	require.Equal(t, 409, status, string(raw))
	stale := completion.RunCompletion
	stale.Run.LeaseOwner = "superseded-worker"
	status, raw = request("POST", "/v1/agents/"+opts.ID+"/runs/"+runID+"/complete", stale, "")
	require.Equal(t, 409, status, string(raw))
	forged := completion.Proof
	forged.Evidence, err = json.Marshal(stale.Run)
	require.NoError(t, err)
	status, raw = request("POST", "/v1/hosts/"+opts.ID+"/runs/"+runID+"/completed", forged, "")
	require.Equal(t, 409, status, string(raw))
	status, raw = request("POST", "/v1/runs/"+runID+"/remote/observe", nil, "")
	require.Equal(t, 202, status, string(raw))
	var observation map[string]string
	require.NoError(t, json.Unmarshal(raw, &observation))
	var report controlplane.RunRecoveryReport
	require.Eventually(t, func() bool {
		code, data := request("GET", "/v1/runs/"+runID+"/remote-reports/"+observation["command_id"], nil, "")
		if code != 200 {
			return false
		}
		if err := json.Unmarshal(data, &report); err != nil {
			t.Errorf("decode owning-agent report: %v", err)
			return false
		}
		return true
	}, 10*time.Second, 50*time.Millisecond)
	var observed bool
	for _, resource := range report.Resources {
		if resource.ExternalID == externalID {
			observed = true
			require.Equal(t, "present", resource.Status)
			require.Equal(t, initial.TopologyUID, resource.OwnerUID)
			require.EqualValues(t, 1, resource.Generation)
			require.NotEmpty(t, resource.ResourceUID)
		}
	}
	require.True(t, observed, "owning-agent live observation must identify the exact Docker container")
	if targetedReset {
		if resetPrepared != nil {
			failPublication.Store(true)
		}
		target := "sysbox_node." + strings.ReplaceAll(topology, "-", "_")
		status, raw = request("POST", "/v1/topologies/"+topology+"/reset", map[string]any{"target": target, "revision": revision["revision"]}, "reset-one")
		require.Equal(t, 202, status, string(raw))
		var reset map[string]string
		require.NoError(t, json.Unmarshal(raw, &reset))
		rootResetID := reset["run_id"]
		if resetPrepared != nil {
			select {
			case <-resetPrepared:
			case <-ctx.Done():
				t.Fatal("reset did not reach durable prepared boundary")
			}
			stopHost()
			require.NoError(t, <-hostDone)
			current.Store(newAPI())
			stopHost, hostDone = startHost()
			originalID := reset["run_id"]
			require.Eventually(t, func() bool {
				r, err := current.Load().apiStore.GetRun(ctx, originalID)
				return err == nil && r.Status == controlplane.RunFailed && r.HostCompletion != nil
			}, 20*time.Second, 50*time.Millisecond)
			failed, err := current.Load().apiStore.GetRun(ctx, originalID)
			require.NoError(t, err)
			require.True(t, failed.Recoverable)
			require.Nil(t, failed.Cleanup)
			status, raw = request("POST", "/v1/runs/"+originalID+"/resume", nil, "")
			require.Equal(t, 202, status, string(raw))
			require.NoError(t, json.Unmarshal(raw, &reset))
			t.Log("cold reset at prepared boundary signed failure; explicit continuation admitted")
		}
		if resetDestroyed != nil {
			defer func() {
				if t.Failed() {
					events, err := current.Load().apiStore.ListAgentCommandEvents(context.Background(), opts.ID)
					t.Logf("reset boundary dispatch events (%v): %+v", err, events)
				}
			}()
			select {
			case <-resetDestroyed:
			case <-ctx.Done():
				t.Fatal("continuation did not reach destroyed boundary")
			}
			stopHost()
			require.NoError(t, <-hostDone)
			current.Store(newAPI())
			stopHost, hostDone = startHost()
			firstID := reset["run_id"]
			require.Eventually(t, func() bool {
				r, err := current.Load().apiStore.GetRun(ctx, firstID)
				return err == nil && r.Status == controlplane.RunFailed && r.HostCompletion != nil
			}, 20*time.Second, 50*time.Millisecond)
			status, raw = request("POST", "/v1/runs/"+firstID+"/resume", nil, "")
			require.Equal(t, 202, status, string(raw))
			require.NoError(t, json.Unmarshal(raw, &reset))
			t.Log("second interrupted executor stopped; explicit continuation preserves original action ancestry")
		}
		var resetRun *controlplane.Run
		defer func() {
			if t.Failed() {
				events, err := current.Load().apiStore.ListAgentCommandEvents(context.Background(), opts.ID)
				t.Logf("reset dispatch events (%v): %+v", err, events)
			}
		}()
		require.Eventually(t, func() bool {
			r, err := current.Load().apiStore.GetRun(ctx, reset["run_id"])
			if err != nil || !r.Status.IsTerminal() {
				return false
			}
			resetRun = r
			return true
		}, 30*time.Second, 50*time.Millisecond)
		require.Equal(t, controlplane.RunDone, resetRun.Status, resetRun.Err)
		require.NotNil(t, resetRun.HostCompletion)
		if resetPrepared != nil {
			require.EqualValues(t, 1, deletions.preparations.Load(), "continuation must reuse original prepared reset handle")
			require.EqualValues(t, 1, deletions.resetDeletes.Load())
			require.EqualValues(t, 1, deletions.replacements.Load())
			actions, err := repository.LoadActions(ctx, topology)
			require.NoError(t, err)
			resets := 0
			for _, action := range actions {
				if action.Action != controlplane.PlanActionReset {
					continue
				}
				resets++
				require.Equal(t, rootResetID, action.RunID)
				require.Equal(t, runtime.ActionSucceeded, action.Status)
			}
			require.Equal(t, 1, resets, "continuation must not create another reset action")
		}
		if resetPrepared == nil {
			require.Equal(t, target, resetRun.Target)
		} else {
			require.NotEmpty(t, resetRun.ParentID)
		}
		require.Equal(t, controlplane.ExecutionScope{TopologyUID: initial.TopologyUID}, resetRun.HostCompletion.Receipt.Grant.Scope)
		replaced, err := repository.LoadState(ctx, topology)
		require.NoError(t, err)
		var replacement string
		for _, resource := range replaced.Resources {
			if resource.Address.String() == target {
				require.Greater(t, resource.Generation, int64(1))
				require.NotEqual(t, externalID, resource.ExternalID)
				replacement = resource.ExternalID
			}
		}
		require.NotEmpty(t, replacement)
		if managedSSH {
			var handle substrate.SSHAccessHandle
			for _, resource := range replaced.Resources {
				if resource.Address.Type != "sysbox_ssh_access" {
					continue
				}
				var saved struct {
					Handle substrate.SSHAccessHandle `json:"handle"`
				}
				raw, err := json.Marshal(resource.Attributes["ssh_access"])
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &saved))
				handle = saved.Handle
				require.EqualValues(t, 2, resource.Generation)
			}
			require.Equal(t, replacement, handle.NodeID)
			require.EqualValues(t, 2, handle.Generation)
			conn, err := docker.Connection(substrate.NodeHandle{ID: replacement, Generation: handle.Generation}, nil)
			require.NoError(t, err)
			inspection, err := conn.(substrate.ConnectionSSHAccess).InspectSSHAccess(ctx, handle)
			require.NoError(t, err)
			require.Equal(t, "present", inspection.Status, "replacement guest must contain rebuilt SSH permission")
			require.Equal(t, handle.Revision, inspection.Revision)
		}
		instances, err = cli.ContainerList(ctx, container.ListOptions{All: true, Filters: owned})
		require.NoError(t, err)
		require.Len(t, instances, 1)
		require.Equal(t, replacement, instances[0].ID)
		t.Log("targeted reset completed with topology parent authority and a replacement node generation")
	}
	if len(destroyPublicationFault) != 0 && destroyPublicationFault[0] {
		failPublication.Store(true)
		status, raw = request("POST", "/v1/topologies/"+topology+"/destroy", nil, "destroy-interrupted")
		require.Equal(t, 202, status, string(raw))
		var accepted map[string]string
		require.NoError(t, json.Unmarshal(raw, &accepted))
		failedID := accepted["run_id"]
		if returned != nil {
			select {
			case <-returned:
			case <-ctx.Done():
				t.Fatal("deletion callback did not reach durable returned boundary")
			}
			stopHost()
			require.NoError(t, <-hostDone)
			before, err := current.Load().apiStore.GetRun(ctx, failedID)
			require.NoError(t, err)
			require.Nil(t, before.HostCompletion, "crashed host must not have completed the Run")
			current.Store(newAPI())
			stopHost, hostDone = startHost()
		}
		require.Eventually(t, func() bool {
			r, err := current.Load().apiStore.GetRun(ctx, failedID)
			return err == nil && r.Status == controlplane.RunFailed && r.HostCompletion != nil
		}, 20*time.Second, 50*time.Millisecond)
		failed, err := current.Load().apiStore.GetRun(ctx, failedID)
		require.NoError(t, err)
		require.True(t, failed.Recoverable)
		require.Nil(t, failed.Cleanup)
		if returned != nil {
			require.Contains(t, failed.Err, "controller stopped after durable deletion return")
		} else {
			require.Contains(t, failed.Err, "injected deletion publication unavailable")
		}
		require.False(t, failPublication.Load())
		require.EqualValues(t, 1, deletions.calls.Load())
		stopHost()
		require.NoError(t, <-hostDone)
		current.Store(newAPI())
		stopHost, hostDone = startHost()
		var recovery map[string]string
		require.Eventually(t, func() bool {
			code, data := request("POST", "/v1/runs/"+failedID+"/remote/recover", nil, "")
			if code != 202 {
				return false
			}
			require.NoError(t, json.Unmarshal(data, &recovery))
			return true
		}, 90*time.Second, 200*time.Millisecond)
		require.Eventually(t, func() bool {
			code, _ := request("GET", "/v1/runs/"+failedID+"/remote-reports/"+recovery["command_id"], nil, "")
			return code == 200
		}, 20*time.Second, 50*time.Millisecond)
		retained, err := repository.LoadState(ctx, topology)
		require.NoError(t, err)
		for _, r := range retained.Resources {
			require.NotEqual(t, externalID, r.ExternalID)
		}
		instances, err = cli.ContainerList(ctx, container.ListOptions{All: true, Filters: owned})
		require.NoError(t, err)
		require.Empty(t, instances)
		require.EqualValues(t, 1, deletions.calls.Load(), "recovery must not invoke provider deletion again")
		t.Log("provider deletion returned; publication failed; signed failed Run recovered by observation without another provider deletion")
	}
	faultOperation.Store("run_destroy")
	drop.Store(true)
	hold.Store(true)
	status, raw = request("POST", "/v1/topologies/"+topology+"/destroy", nil, "destroy-one")
	require.Equal(t, 202, status, string(raw))
	var destruction map[string]any
	require.NoError(t, json.Unmarshal(raw, &destruction))
	destroyID := destruction["run_id"].(string)
	var destroyed *controlplane.Run
	require.Eventually(t, func() bool {
		r, err := current.Load().apiStore.GetRun(ctx, destroyID)
		if err != nil || !r.Status.IsTerminal() {
			return false
		}
		destroyed = r
		return true
	}, 20*time.Second, 50*time.Millisecond)
	require.Equal(t, controlplane.RunDone, destroyed.Status, destroyed.Err)
	require.NotNil(t, destroyed.Cleanup, "destroy completion must persist an owning-agent empty-state receipt")
	var destroyProof hostRunCompletion
	select {
	case report := <-lost:
		require.Equal(t, destroyID, report.Run.ID)
		require.NotNil(t, report.Cleanup)
		destroyProof = report.Proof
	case <-ctx.Done():
		t.Fatal("destroy completion response was not dropped")
	}
	require.Equal(t, initial.TopologyUID, destroyed.Cleanup.TopologyUID)
	require.Greater(t, destroyed.Cleanup.StateSerial, int64(0))
	instances, err = cli.ContainerList(ctx, container.ListOptions{All: true, Filters: owned})
	require.NoError(t, err)
	require.Empty(t, instances)
	// A cold API must use the persisted receipt, not a memory projection.
	stopHost()
	require.NoError(t, <-hostDone)
	third := newAPI()
	current.Store(third)
	stopHost, hostDone = startHost()
	hold.Store(false)
	awaitReceipt(destroyProof)
	status, raw = request("DELETE", "/v1/topologies/"+topology, nil, "")
	require.Equal(t, 200, status, string(raw))
	placement, err := third.workspaceService().loadPlacement(topology)
	require.NoError(t, err)
	require.Nil(t, placement)
	t.Logf("host supervisor created %s; completion acknowledgment lost; reconstructed API/Agent/supervisor retained run %s and one container; stale claim/report rejected", externalID, runID)
	t.Log("host-signed destroy committed cleanup receipt; cold API deleted metadata without force")
}
