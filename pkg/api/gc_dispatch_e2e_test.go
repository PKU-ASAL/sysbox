//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/stretchr/testify/require"
)

func TestRemoteDockerGCDispatchAndLostReceiptE2E(t *testing.T)  { testRemoteDockerGCDispatch(t, false) }
func TestRemoteDockerGCBatchSequentialRecoveryE2E(t *testing.T) { testRemoteDockerGCDispatch(t, true) }
func TestRemoteDockerGCExternalSourceFencingE2E(t *testing.T) {
	testRemoteDockerGCDispatch(t, false, true)
}
func testRemoteDockerGCDispatch(t *testing.T, batch bool, external ...bool) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires isolated real Docker resources")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	private := func() string { dir := t.TempDir(); require.NoError(t, os.Chmod(dir, 0700)); return dir }
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = private()
	cfg.Paths.WorkspacesDir = private()
	cfg.State.Backend = "sqlite://" + filepath.Join(private(), "api.db")
	newAPI := func() *Server {
		s := NewServerWithConfig(cfg)
		require.NoError(t, s.initErr)
		require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
		s.grantHostKeys = map[string]ed25519.PublicKey{"gc-agent": hostPub}
		return s
	}
	current := atomic.Pointer[Server]{}
	first := newAPI()
	current.Store(first)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { current.Load().ServeHTTP(w, r) }))
	defer httpServer.Close()
	repo, err := first.actionRepository("gc-lab")
	require.NoError(t, err)
	refs, err := repo.LoadGCReferences(ctx, "gc-lab")
	require.NoError(t, err)
	require.NoError(t, first.workspaceService().bindPlacement("gc-lab", "gc-agent", controlplane.AgentProtocolVersion))
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	defer cli.Close()
	labels := map[string]string{"sysbox.managed": "true", "sysbox.owner_uid": refs.TopologyUID, "sysbox.topology_uid": refs.TopologyUID, "sysbox.resource_uid": "orphan-uid", "sysbox.generation": "1"}
	created, err := cli.ContainerCreate(ctx, &container.Config{Image: "alpine:3.22", Cmd: []string{"true"}, Labels: labels}, &container.HostConfig{NetworkMode: "none"}, nil, nil, "")
	require.NoError(t, err)
	defer cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	secondID := ""
	if batch {
		labels["sysbox.resource_uid"] = "orphan-two"
		second, err := cli.ContainerCreate(ctx, &container.Config{Image: "alpine:3.22", Cmd: []string{"true"}, Labels: labels}, &container.HostConfig{NetworkMode: "none"}, nil, nil, "")
		require.NoError(t, err)
		secondID = second.ID
		defer cli.ContainerRemove(context.Background(), secondID, container.RemoveOptions{Force: true})
	}

	if len(external) > 0 {
		publish := func(operation string, expected int64, manifest *persistence.GCSourceManifest) {
			raw, err := json.Marshal(gcSourcePublication{TopologyUID: refs.TopologyUID, ExpectedVersion: &expected, Manifest: manifest})
			require.NoError(t, err)
			response, err := http.Post(httpServer.URL+"/v1/topologies/gc-lab/gc/sources/remote/"+operation, "application/json", bytes.NewReader(raw))
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, 200, response.StatusCode)
		}
		publish("begin", 0, nil)
		manifest := persistence.GCSourceManifest{Domains: map[string][]string{"revision": {"fixture-revision"}, "artifact": {}, "cache": {}, "placement": {"gc-agent"}}, ValidUntil: time.Now().Add(time.Hour), References: []persistence.GCResourceReference{{Source: "revision/node", ResourceType: "sysbox_node", Driver: "docker", ExternalID: created.ID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan-uid", Generation: 1}}}
		publish("complete", 1, &manifest)
		protected, err := repo.LoadGCReferences(ctx, "gc-lab")
		require.NoError(t, err)
		candidate := controlplane.GCCandidate{Kind: "container", ExternalID: created.ID, TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan-uid", Generation: 1, InventoryDigest: protected.Digest}
		require.ErrorIs(t, repo.ReserveGC(ctx, "gc-lab", persistence.GCReservation{ID: "protected", AgentID: "gc-agent", Candidate: candidate}), persistence.ErrConflict)
		_, err = cli.ContainerInspect(ctx, created.ID)
		require.NoError(t, err)
		publish("begin", 2, nil)
		manifest.References = nil
		publish("complete", 3, &manifest)
		refs, err = repo.LoadGCReferences(ctx, "gc-lab")
		require.NoError(t, err)
	}
	provider, err := dockerprovider.New()
	require.NoError(t, err)
	defer provider.Close()
	authority := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "gc-agent", Secret: "gc-secret", APIURL: httpServer.URL}}
	root := private()
	socket := filepath.Join(private(), "host.sock")
	keyFile := filepath.Join(private(), "issuer.pub")
	require.NoError(t, os.WriteFile(keyFile, pub, 0600))
	entered, proceed := make(chan struct{}), make(chan struct{})
	var enteredOnce sync.Once
	var deletes atomic.Int32
	var hold atomic.Bool
	hold.Store(true)
	receiptSent := make(chan struct{}, 1)
	coordinator := &hostsupervisor.GCCoordinator{Root: root, AgentID: "gc-agent", HostBootID: "host-boot", IssuerKey: pub, HostKey: hostKey, Authorize: authority.Authorize, ValidateReservation: func(ctx context.Context, _ controlplane.GCCandidate, g controlplane.SignedExecutionGrant) error {
		return authority.Authorize(ctx, g)
	}, ReportStop: func(ctx context.Context, r controlplane.SignedExecutionStopReceipt) error {
		if err := authority.ReportStop(ctx, r); err != nil {
			return err
		}
		select {
		case receiptSent <- struct{}{}:
		default:
		}
		if hold.Load() {
			return fmt.Errorf("injected lost completion acknowledgement")
		}
		return nil
	}}
	host := &hostsupervisor.Server{Socket: socket, GroupID: -1, GC: coordinator, GCEffect: func(ctx context.Context, candidate controlplane.GCCandidate) (json.RawMessage, error) {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-proceed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		deletes.Add(1)
		if err := provider.DeleteManagedExternal(ctx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"deleted":true}`), nil
	}}
	hostCtx, stopHost := context.WithCancel(ctx)
	hostDone := make(chan error, 1)
	go func() { hostDone <- host.Serve(hostCtx) }()
	defer func() { stopHost(); require.NoError(t, <-hostDone) }()
	require.Eventually(t, func() bool { _, err := os.Stat(socket); return err == nil }, time.Second, 10*time.Millisecond)
	workerCfg := cfg
	workerCfg.Paths.RunsDir = private()
	workerCfg.Paths.WorkspacesDir = private()
	workerCfg.State.Backend = "sqlite://" + filepath.Join(private(), "worker.db")
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	opts := agentexec.Options{ID: "gc-agent", Secret: "gc-secret", APIURL: httpServer.URL, Capabilities: []string{"docker"}, PollInterval: 20 * time.Millisecond, ExecutionSessionFile: filepath.Join(private(), "session.json"), ExecutionGrantPublicKeyFile: keyFile, HostSupervisorSocket: socket, RequireHostSupervisor: true}
	go func() { workerDone <- agentexec.Run(workerCtx, opts, NewExecutionBridge(workerCfg)) }()
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Error("Agent did not stop")
		}
	}()
	require.Eventually(t, func() bool {
		a, err := first.apiStore.GetAgent(ctx, "gc-agent")
		return err == nil && a != nil && a.Status == "online"
	}, 5*time.Second, 20*time.Millisecond)
	candidate := controlplane.GCCandidate{Kind: "container", ExternalID: created.ID, TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan-uid", Generation: 1, InventoryDigest: refs.Digest}
	raw, err := json.Marshal(candidate)
	require.NoError(t, err)
	endpoint := "/v1/topologies/gc-lab/gc"
	if batch {
		other := candidate
		other.ExternalID = secondID
		other.ResourceUID = "orphan-two"
		raw, err = json.Marshal(gcBatchRequest{Candidates: []controlplane.GCCandidate{candidate, other}})
		require.NoError(t, err)
		endpoint += "/batch"
	}
	req := httptest.NewRequest("POST", endpoint, bytes.NewReader(raw))
	req.Header.Set("Idempotency-Key", "delete-one")
	response := httptest.NewRecorder()
	first.ServeHTTP(response, req)
	require.Equal(t, 202, response.Code, response.Body.String())
	var reservation persistence.GCReservation
	if batch {
		var accepted gcBatchResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &accepted))
		require.Len(t, accepted.Reservations, 2)
		reservation = accepted.Reservations[0]
	} else {
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &reservation))
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("owning Agent did not dispatch GC")
	}
	manager, err := first.stateManager("gc-lab")
	require.NoError(t, err)
	st, err := manager.Load()
	require.NoError(t, err)
	require.ErrorIs(t, manager.Save(st), persistence.ErrConflict)
	_, err = manager.Snapshot(ctx, "new reference while deleting")
	require.ErrorIs(t, err, persistence.ErrConflict)

	if len(external) > 0 {
		_, err = repo.BeginGCSource(ctx, "gc-lab", "remote", refs.TopologyUID, 4)
		require.ErrorIs(t, err, persistence.ErrConflict)
	}
	close(proceed)
	select {
	case <-receiptSent:
	case <-ctx.Done():
		t.Fatal("host completion not received")
	}
	reopened := newAPI()
	current.Store(reopened)
	hold.Store(false)
	cold := *coordinator
	require.NoError(t, cold.Recover(ctx))
	reopenedRepo, err := reopened.actionRepository("gc-lab")
	require.NoError(t, err)
	completed, err := reopenedRepo.LoadGCReservation(ctx, "gc-lab", reservation.ID)
	require.NoError(t, err)
	require.NotEmpty(t, completed.ReceiptHash)
	command, err := readFailureCommand(ctx, reopened.apiStore, reservation.ID)
	require.NoError(t, err)
	require.True(t, command.Retired)
	require.Equal(t, controlplane.AgentCommandStatusCompleted, command.Command.Status)
	if batch {
		require.Eventually(t, func() bool {
			all, err := reopenedRepo.ListGCReservations(ctx, "gc-lab")
			if err != nil || len(all) != 2 {
				return false
			}
			for _, r := range all {
				if r.ReceiptHash == "" {
					return false
				}
			}
			return true
		}, 10*time.Second, 20*time.Millisecond)
		require.EqualValues(t, 2, deletes.Load())
		_, err = cli.ContainerInspect(ctx, secondID)
		require.True(t, errdefs.IsNotFound(err))
	} else {
		require.EqualValues(t, 1, deletes.Load())
	}
	_, err = cli.ContainerInspect(ctx, created.ID)
	require.True(t, errdefs.IsNotFound(err), "%v", err)

	require.NoError(t, manager.Save(st))
	if len(external) > 0 {
		_, err = reopenedRepo.BeginGCSource(ctx, "gc-lab", "remote", refs.TopologyUID, 4)
		require.NoError(t, err)
	}
	t.Log("real Docker GC dispatched through API/Agent/host; concurrent references blocked; exact signed receipt recovered after API reconstruction; one delete and independent absence")
}
