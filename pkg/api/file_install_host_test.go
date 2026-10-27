package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileHostReceiptFinishesAfterCancelWithoutReleasingUnknownGrant(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := repo.OpenSession(ctx, "agent", "first", "")
		require.NoError(t, err)
		op := controlplane.GuestFileOperation{ID: "install", Version: 1, Topology: "lab", Node: "node", AgentID: "agent", CommandID: "command", Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now()}
		cmd := controlplane.AgentCommand{ID: op.CommandID, AgentID: op.AgentID, Type: "guest_file_put", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, ActionID: op.ID, FilePut: &controlplane.GuestFilePut{ID: op.ID, Topology: op.Topology, Node: op.Node, Path: "/opt/files/data", Mode: 0600, Size: 3, SHA256: strings.Repeat("a", 64)}}
		require.NoError(t, store.CreateGuestFileDispatch(ctx, op, cmd))
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		svc := newGuestFileOperationService(store)
		_, err = svc.MarkRunning(ctx, op.ID)
		require.NoError(t, err)
		spec, err := controlplane.FromFileInstallCommand(cmd)
		require.NoError(t, err)
		digest, err := spec.Digest()
		require.NoError(t, err)
		g, err := repo.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: cmd.ID, ActionID: op.ID, Attempt: uint64(leased.Attempt), Operation: "guest_file_install", TargetBootID: "guest-boot", Digest: digest}, time.Minute)
		require.NoError(t, err)
		op, err = svc.RequestCancel(ctx, op.ID)
		require.NoError(t, err)
		noEffect, err := s.cancelFileDispatch(ctx, op)
		require.NoError(t, err)
		require.False(t, noEffect)
		require.ErrorIs(t, repo.Authorize(ctx, *g), persistence.ErrConflict)
		_, err = repo.OpenSession(ctx, "agent", "second", session.ID)
		require.NoError(t, err)
		result := substrate.FileInstallResult{Outcome: substrate.FileInstallCommitted, Quiescent: true, SHA256: spec.SHA256, Size: spec.Size, Mode: spec.Mode}
		receipt, err := controlplane.SignExecutionFileStopReceipt(key, *g, "host-boot", "guest-boot", op.ID, digest, result, "receipt", time.Now())
		require.NoError(t, err)
		// Simulate a crash after committing file metadata but before sealing grant.
		_, err = store.CompleteFileFromHost(ctx, "agent", op.ID, receipt)
		require.NoError(t, err)
		activities, err := store.ListGuestActivity(ctx, "lab")
		require.NoError(t, err)
		require.Len(t, activities, 1)
		require.True(t, activities[0].Active())
		post := func(r controlplane.SignedExecutionStopReceipt) *httptest.ResponseRecorder {
			raw, e := json.Marshal(r)
			require.NoError(t, e)
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
			return w
		}
		response := post(receipt)
		require.Equal(t, 200, response.Code, response.Body.String())
		response = post(receipt)
		require.Equal(t, 200, response.Code, response.Body.String())
		activities, err = store.ListGuestActivity(ctx, "lab")
		require.NoError(t, err)
		require.False(t, activities[0].Active())
		completed, err := store.GetGuestFileOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.GuestExecutionCompleted, completed.Status)
		require.Equal(t, result, *completed.Result)
	})
}

func TestFileCancelAndGrantIssueSerialize(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := &Server{apiStore: store}
		repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := repo.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		op := controlplane.GuestFileOperation{ID: "install", Version: 1, AgentID: "agent", CommandID: "command", Topology: "lab", Node: "node", Status: controlplane.GuestExecutionQueued}
		cmd := controlplane.AgentCommand{ID: op.CommandID, AgentID: op.AgentID, Type: "guest_file_put", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, ActionID: op.ID, FilePut: &controlplane.GuestFilePut{ID: op.ID, Topology: op.Topology, Node: op.Node, Path: "/tmp/data", Mode: 0600, SHA256: strings.Repeat("a", 64)}}
		require.NoError(t, store.CreateGuestFileDispatch(ctx, op, cmd))
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		var issueErr, cancelErr error
		var noEffect bool
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			var request controlplane.ExecutionGrant
			storage := apiExecutionGrantStorage{store: store, commandID: cmd.ID, validate: func(record *persistence.CommandRecord) error {
				var err error
				request, err = s.sourceGrant(ctx, "agent", ExecutionGrantRequest{SessionID: session.ID, Attempt: leased.Attempt, LeaseOwner: leased.LeaseOwner, BootID: "boot"}, record)
				return err
			}}
			issueErr = storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
				memory := &grantTransaction{raw: raw}
				_, err := persistence.NewExecutionGrantRepository(memory).Issue(ctx, request, time.Minute)
				return memory.raw, err
			})
		}()
		go func() { defer wait.Done(); noEffect, cancelErr = s.cancelFileDispatch(ctx, op) }()
		wait.Wait()
		require.NoError(t, cancelErr)
		if noEffect {
			require.Error(t, issueErr)
		} else {
			require.NoError(t, issueErr)
			grant, err := repo.InspectGrant(ctx, "agent", controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1})
			require.NoError(t, err)
			require.Equal(t, controlplane.GrantRevoked, grant.State)
		}
	})
}

func TestActiveFileCancelResponseReportsPendingRequest(t *testing.T) {
	s := guestExecutionTestServer(t)
	ctx := context.Background()
	session, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).OpenSession(ctx, "host-a", "guest-process", "")
	require.NoError(t, err)
	r := httptest.NewRequest("PUT", "/v1/topologies/lab/nodes/web/files?path=/opt/data/file&mode=0600", strings.NewReader("data"))
	r.Header.Set("X-Sysbox-Roles", "admin")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, 202, w.Code, w.Body.String())
	var op controlplane.GuestFileOperation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &op))
	stored, err := s.apiStore.GetGuestFileOperation(ctx, op.ID)
	require.NoError(t, err)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "host-a", stored.CommandID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = newGuestFileOperationService(s.apiStore).MarkRunning(ctx, op.ID)
	require.NoError(t, err)
	request, err := s.sourceGrant(ctx, "host-a", ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "guest"}, &persistence.CommandRecord{Command: *cmd})
	require.NoError(t, err)
	_, err = persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).Issue(ctx, request, time.Minute)
	require.NoError(t, err)
	r = httptest.NewRequest("POST", "/v1/file-operations/"+op.ID+"/cancel", nil)
	r.Header.Set("X-Sysbox-Roles", "admin")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, 202, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &op))
	require.True(t, op.CancelRequested)
	require.Equal(t, controlplane.GuestExecutionRunning, op.Status)
}

func TestRebootedFileReceiptEndsUnknownAndAllowsSuccessor(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "host", "")
		require.NoError(t, err)
		op := controlplane.GuestFileOperation{ID: "file", Version: 1, AgentID: "agent", CommandID: "command", Topology: "lab", Node: "node", Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now().UTC()}
		cmd := controlplane.AgentCommand{ID: op.CommandID, AgentID: op.AgentID, Type: "guest_file_put", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, ActionID: op.ID, FilePut: &controlplane.GuestFilePut{RebootScope: "guest-tmpfs", ID: op.ID, Topology: "lab", Node: "node", Path: "/tmp/file", Mode: 0600, SHA256: strings.Repeat("a", 64)}}
		require.NoError(t, store.CreateGuestFileDispatch(ctx, op, cmd))
		request, err := controlplane.FromFileInstallCommand(cmd)
		require.NoError(t, err)
		digest, err := request.Digest()
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: cmd.ID, ActionID: op.ID, Attempt: 1, Operation: "guest_file_install", Digest: digest, TargetBootID: "old-guest"}, time.Minute)
		require.NoError(t, err)
		require.NoError(t, authority.Revoke(ctx, *grant))
		receipt, err := controlplane.SignRebootedFileStopReceipt(key, *grant, "host", "new-guest", "receipt", time.Now())
		require.NoError(t, err)
		post := func(r controlplane.SignedExecutionStopReceipt) *httptest.ResponseRecorder {
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
			return w
		}
		wrong := receipt
		wrong.ObservedGuestBootID = "old-guest"
		require.Equal(t, 401, post(wrong).Code)
		response := post(receipt)
		require.Equal(t, 200, response.Code, response.Body.String())
		ended, err := store.GetGuestFileOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.GuestExecutionFailed, ended.Status)
		require.Equal(t, substrate.FileInstallUnknown, ended.Result.Outcome)
		require.Contains(t, ended.Error, "unknown")
		require.True(t, ended.Result.Quiescent)
		next := *grant
		next.ActionID = "next"
		next.CommandID = "next-command"
		next.TargetBootID = "new-guest"
		successor, err := authority.Issue(ctx, next, time.Minute)
		require.NoError(t, err)
		require.Equal(t, 200, post(receipt).Code)
		require.NoError(t, authority.Authorize(ctx, *successor))
	})
}

func TestSameBootFileScopeStopEndsUnknownAndAllowsSuccessor(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "host", "")
		require.NoError(t, err)
		op := controlplane.GuestFileOperation{ID: "file", Version: 1, AgentID: "agent", CommandID: "command", Topology: "lab", Node: "node", Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now().UTC()}
		cmd := controlplane.AgentCommand{ID: op.CommandID, AgentID: op.AgentID, Type: "guest_file_put", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, ActionID: op.ID, FilePut: &controlplane.GuestFilePut{ID: op.ID, Topology: "lab", Node: "node", Path: "/tmp/file", Mode: 0600, SHA256: strings.Repeat("a", 64)}}
		require.NoError(t, store.CreateGuestFileDispatch(ctx, op, cmd))
		request, err := controlplane.FromFileInstallCommand(cmd)
		require.NoError(t, err)
		digest, err := request.Digest()
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: cmd.ID, ActionID: op.ID, Attempt: 1, Operation: "guest_file_install", Digest: digest, TargetBootID: "old-guest"}, time.Minute)
		require.NoError(t, err)
		require.NoError(t, authority.Revoke(ctx, *grant))
		receipt, err := controlplane.SignExecutionFileStopReceipt(key, *grant, "host", "old-guest", op.ID, digest, substrate.FileInstallResult{Outcome: substrate.FileInstallUnknown, Quiescent: true, StoppedEpoch: grant.Epoch, SHA256: request.SHA256, Size: request.Size, Mode: request.Mode}, "receipt", time.Now())
		require.NoError(t, err)
		post := func(r controlplane.SignedExecutionStopReceipt) *httptest.ResponseRecorder {
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
			return w
		}
		wrong := receipt
		wrong.ObservedGuestBootID = "old-guest"
		require.Equal(t, 401, post(wrong).Code)
		response := post(receipt)
		require.Equal(t, 200, response.Code, response.Body.String())
		ended, err := store.GetGuestFileOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.GuestExecutionFailed, ended.Status)
		require.Equal(t, substrate.FileInstallUnknown, ended.Result.Outcome)
		require.Contains(t, ended.Error, "unknown")
		require.True(t, ended.Result.Quiescent)
		next := *grant
		next.ActionID = "next"
		next.CommandID = "next-command"
		next.TargetBootID = "old-guest"
		successor, err := authority.Issue(ctx, next, time.Minute)
		require.NoError(t, err)
		require.Equal(t, 200, post(receipt).Code)
		require.NoError(t, authority.Authorize(ctx, *successor))
	})
}
