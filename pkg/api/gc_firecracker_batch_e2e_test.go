//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	fcprovider "github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"

	"path/filepath"

	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteFirecrackerGCBatchRecoveryE2E(t *testing.T) {
	batch := true
	for _, name := range []string{"SYSBOX_FIRECRACKER_BIN", "SYSBOX_FIRECRACKER_KERNEL", "SYSBOX_FIRECRACKER_ROOTFS"} {
		if os.Getenv(name) == "" {
			t.Skip("requires explicit real Firecracker artifacts")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
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
	refs, err = repo.LoadGCReferences(ctx, "gc-lab")
	require.NoError(t, err)

	fcConfig := config.DefaultServiceConfig()
	fcConfig.Providers.Firecracker.Binary = os.Getenv("SYSBOX_FIRECRACKER_BIN")
	configPath := filepath.Join(private(), "config.json")
	configRaw, err := json.Marshal(fcConfig)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configRaw, 0600))
	t.Setenv("SYSBOX_CONFIG", configPath)
	provider := fcprovider.New(os.Getenv("SYSBOX_FIRECRACKER_KERNEL"), private())
	handles := map[string]substrate.NodeHandle{}
	proofs := map[string]gcProcessProof{}
	create := func(uid string) string {
		id := substrate.OperationIdentity{OwnerUID: refs.TopologyUID, TopologyUID: refs.TopologyUID, ResourceUID: uid, Generation: 1, ActionID: "create-" + uid}
		scoped := substrate.WithOperationIdentity(ctx, id)
		h, err := provider.CreateNode(scoped, substrate.NodeSpec{Name: uid, Image: substrate.ArtifactHandle{ID: os.Getenv("SYSBOX_FIRECRACKER_ROOTFS")}, ProviderConfig: &fcprovider.Config{Kernel: os.Getenv("SYSBOX_FIRECRACKER_KERNEL"), AllowDirect: true}})
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			require.NoError(t, provider.DestroyNode(cleanup, h))
		})
		require.NoError(t, provider.StartNode(scoped, h))
		obs, err := provider.ObserveNode(scoped, h)
		require.NoError(t, err)
		proofs[h.ID] = captureGCProcess(t, obs.PID, h.Provider.(*fcprovider.HandleState).Socket)
		handles[h.ID] = h
		return h.ID
	}
	createdID := create("orphan-uid")
	secondID := create("orphan-two")
	survivorID := create("survivor")
	present := func(id string) bool {
		obs, err := provider.ObserveNode(ctx, handles[id])
		require.NoError(t, err)
		return obs.Exists
	}

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
	receiptSent := make(chan controlplane.SignedExecutionStopReceipt, 32)
	replayed := make(chan controlplane.SignedExecutionStopReceipt, 32)
	coordinator := &hostsupervisor.GCCoordinator{Root: root, AgentID: "gc-agent", HostBootID: "host-boot", IssuerKey: pub, HostKey: hostKey, Authorize: authority.Authorize, ValidateReservation: func(ctx context.Context, _ controlplane.GCCandidate, g controlplane.SignedExecutionGrant) error {
		return authority.Authorize(ctx, g)
	}, ReportStop: func(ctx context.Context, r controlplane.SignedExecutionStopReceipt) error {
		if err := authority.ReportStop(ctx, r); err != nil {
			return err
		}
		loseAck := hold.Load()
		if loseAck {
			select {
			case receiptSent <- r:
			default:
			}
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
	defer func() {
		stopHost()
		if hostDone != nil {
			require.NoError(t, <-hostDone)
		}
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(socket); return err == nil }, time.Second, 10*time.Millisecond)
	workerCfg := cfg
	workerCfg.Paths.RunsDir = private()
	workerCfg.Paths.WorkspacesDir = private()
	workerCfg.State.Backend = "sqlite://" + filepath.Join(private(), "worker.db")
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	opts := agentexec.Options{ID: "gc-agent", Secret: "gc-secret", APIURL: httpServer.URL, Capabilities: []string{"firecracker"}, PollInterval: 20 * time.Millisecond, ExecutionSessionFile: filepath.Join(private(), "session.json"), ExecutionGrantPublicKeyFile: keyFile, HostSupervisorSocket: socket, RequireHostSupervisor: true}
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
	candidate := controlplane.GCCandidate{Kind: "firecracker", ExternalID: createdID, TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan-uid", Generation: 1, InventoryDigest: refs.Digest}
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

	close(proceed)
	lost := map[string]controlplane.SignedExecutionStopReceipt{}
	for len(lost) < 2 {
		select {
		case receipt := <-receiptSent:
			lost[receipt.Grant.CommandID] = receipt
		case <-ctx.Done():
			t.Fatal("both batch receipts must hit lost acknowledgement boundary")
		}
	}

	stopHost()
	hostErr := <-hostDone
	hostDone = nil
	require.NoError(t, hostErr)
	reopened := newAPI()
	current.Store(reopened)
	hold.Store(false)
	cold := *coordinator
	originalReport := coordinator.ReportStop
	cold.ReportStop = func(ctx context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
		if err := originalReport(ctx, receipt); err != nil {
			return err
		}
		select {
		case replayed <- receipt:
		default:
		}
		return nil
	}
	expected, err := repo.LoadGCReservation(ctx, "gc-lab", reservation.ID)
	require.NoError(t, err)
	require.NoError(t, cold.Recover(ctx))
	recovered := map[string]bool{}
	require.Eventually(t, func() bool {
		for {
			select {
			case receipt := <-replayed:
				original, ok := lost[receipt.Grant.CommandID]
				require.True(t, ok)
				require.Equal(t, original, receipt)
				recovered[receipt.Grant.CommandID] = true
			default:
				return len(recovered) == 2
			}
		}
	}, time.Second, 10*time.Millisecond, "both original signed receipts must replay")

	reopenedRepo, err := reopened.actionRepository("gc-lab")
	require.NoError(t, err)
	completed, err := reopenedRepo.LoadGCReservation(ctx, "gc-lab", reservation.ID)
	require.NoError(t, err)
	require.NotEmpty(t, completed.ReceiptHash)
	require.Equal(t, expected.ReceiptHash, completed.ReceiptHash, "recovery preserves exact signed receipt")
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
		require.False(t, present(secondID))
	} else {
		require.EqualValues(t, 1, deletes.Load())
	}
	require.False(t, present(createdID))
	requireGCProcessGone(t, proofs[createdID])
	requireGCProcessGone(t, proofs[secondID])
	require.True(t, present(survivorID))
	survivor, err := provider.ObserveNode(ctx, handles[survivorID])
	require.NoError(t, err)
	require.True(t, survivor.Running)

	require.NoError(t, manager.Save(st))

	t.Log("real Firecracker batch GC dispatched through API/Agent/host; concurrent references blocked; exact signed receipt recovered after API reconstruction; two exact deletes and independent absence")
}
