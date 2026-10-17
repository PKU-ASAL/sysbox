package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGCHTTPDispatchUsesOwningAgentAndReplaysOriginalCommand(t *testing.T) {
	s := guestExecutionTestServer(t)
	ctx := context.Background()
	_, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
	host, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys["host-a"] = host
	require.NoError(t, s.workspaceService().bindPlacement("lab", "host-a", controlplane.AgentProtocolVersion))
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	c := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan-uid", Generation: 1, InventoryDigest: refs.Digest}
	post := func(candidate controlplane.GCCandidate) *httptest.ResponseRecorder {
		raw, err := json.Marshal(candidate)
		require.NoError(t, err)
		req := httptest.NewRequest("POST", "/v1/topologies/lab/gc", bytes.NewReader(raw))
		req.Header.Set("Idempotency-Key", "cleanup-1")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	first := post(c)
	require.Equal(t, 202, first.Code, first.Body.String())
	var reservation persistence.GCReservation
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &reservation))
	require.Equal(t, "host-a", reservation.AgentID)
	second := post(c)
	require.Equal(t, 202, second.Code, second.Body.String())
	var replay persistence.GCReservation
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &replay))
	require.Equal(t, reservation, replay)
	commands, err := s.apiStore.ListAgentCommands(ctx, "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Equal(t, "gc_delete", commands[0].Type)
	require.Equal(t, reservation.ID, commands[0].ID)
	c.ExternalID = "replacement"
	require.Equal(t, 409, post(c).Code)
}

func TestGCCompletionCannotReleaseReservationFromWorkerOrGenericStop(t *testing.T) {
	s := guestExecutionTestServer(t)
	ctx := context.Background()
	host, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys["host-a"] = host
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	c := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan-uid", Generation: 1, InventoryDigest: refs.Digest}
	reservation := persistence.GCReservation{ID: "gc-complete", AgentID: "host-a", Candidate: c}
	require.NoError(t, s.reserveGCWithAuthority(ctx, "lab", reservation))
	require.NoError(t, s.publishGCReservation(ctx, "lab", reservation))
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := authority.OpenSession(ctx, "host-a", "guest-process", "")
	require.NoError(t, err)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "host-a", reservation.ID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	grant, err := s.issueCommandGrant(ctx, "host-a", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "boot"})
	require.NoError(t, err)
	generic, err := controlplane.SignExecutionStopReceipt(key, *grant, "boot", "generic", time.Now())
	require.NoError(t, err)
	post := func(receipt controlplane.SignedExecutionStopReceipt) *httptest.ResponseRecorder {
		raw, err := json.Marshal(receipt)
		require.NoError(t, err)
		req := httptest.NewRequest("POST", "/v1/hosts/host-a/execution-grants/stopped", bytes.NewReader(raw))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	require.NotEqual(t, 200, post(generic).Code)
	receipt, err := controlplane.SignGCDeletionReceipt(key, *grant, "boot", "receipt", c, time.Now())
	require.NoError(t, err)
	first := post(receipt)
	require.Equal(t, 200, first.Code, first.Body.String())
	require.Equal(t, 200, post(receipt).Code)
	completed, err := repo.LoadGCReservation(ctx, "lab", reservation.ID)
	require.NoError(t, err)
	require.NotEmpty(t, completed.ReceiptHash)
	stored, err := readFailureCommand(ctx, s.apiStore, reservation.ID)
	require.NoError(t, err)
	require.True(t, stored.Retired)
	require.Equal(t, controlplane.AgentCommandStatusCompleted, stored.Command.Status)
	_, err = s.issueCommandGrant(ctx, "host-a", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "boot"})
	require.Error(t, err)
}

func TestGCCompletionRetirementSurvivesInterruptedAuthorityRelease(t *testing.T) {
	s := guestExecutionTestServer(t)
	ctx := context.Background()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	candidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: refs.Digest}
	reservation := persistence.GCReservation{ID: "gc-retire", AgentID: "host-a", Candidate: candidate}
	require.NoError(t, s.reserveGCWithAuthority(ctx, "lab", reservation))
	require.NoError(t, s.publishGCReservation(ctx, "lab", reservation))
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := authority.OpenSession(ctx, "host-a", "guest-process", "")
	require.NoError(t, err)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "host-a", reservation.ID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "boot"}
	grant, err := s.issueCommandGrant(ctx, "host-a", cmd.ID, proof)
	require.NoError(t, err)
	receipt, err := controlplane.SignGCDeletionReceipt(key, *grant, "boot", "receipt", candidate, time.Now())
	require.NoError(t, err)
	// Reproduce a process exit after the first completion commit (retirement).
	require.NoError(t, s.apiStore.DeleteAgentCommand(ctx, "host-a", cmd.ID))
	_, err = s.issueCommandGrant(ctx, "host-a", cmd.ID, proof)
	require.Error(t, err)
	manager, err := s.stateManager("lab")
	require.NoError(t, err)
	st, err := manager.Load()
	require.NoError(t, err)
	require.ErrorIs(t, manager.Save(st), persistence.ErrConflict)
	require.NoError(t, s.completeGCFromHost(ctx, receipt, pub))
	_, err = s.issueCommandGrant(ctx, "host-a", cmd.ID, proof)
	require.Error(t, err)
	require.NoError(t, manager.Save(st))
}

func TestGCGrantRequestCannotInventDispatchOrPoisonReservation(t *testing.T) {
	s := guestExecutionTestServer(t)
	ctx := context.Background()
	_, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
	owner, err := s.apiStore.GetAgent(ctx, "host-a")
	require.NoError(t, err)
	owner.AuthSecret = "secret"
	owner.SecretHash = agent.SecretHash("secret")
	require.NoError(t, s.apiStore.SaveAgent(ctx, *owner))
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := authority.OpenSession(ctx, "host-a", "guest-process", "")
	require.NoError(t, err)
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	candidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: refs.Digest}
	require.NoError(t, s.reserveGCWithAuthority(ctx, "lab", persistence.GCReservation{ID: "gc-reserved", AgentID: "host-a", Candidate: candidate}))
	raw, err := json.Marshal(gcGrantRequest{Candidate: candidate, Topology: "lab", SessionID: session.ID, ActionID: "poison", CommandID: "gc-reserved", Attempt: 1, LeaseOwner: "invented", BootID: "boot"})
	require.NoError(t, err)
	request := httptest.NewRequest("POST", "/v1/agents/host-a/gc-grant", bytes.NewReader(raw))
	require.NoError(t, agent.SignRequest(request, "host-a", "secret", time.Now()))
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	require.Equal(t, 409, response.Code, response.Body.String())
	commands, err := s.apiStore.ListAgentCommands(ctx, "host-a")
	require.NoError(t, err)
	require.Empty(t, commands)
}
