package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGrantSigningKeyIsLoadedFromOperatorConfig(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "issuer.key")
	require.NoError(t, os.WriteFile(path, key, 0600))
	cfg := config.MustLoadServiceConfig("")
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.API.ExecutionGrant.SigningKeyFile = path
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	require.Equal(t, key, s.grantSigner)
	require.NoError(t, os.Chmod(path, 0644))
	s = NewServerWithConfig(cfg)
	require.Error(t, s.initErr)
}

func TestGrantSessionRequiresProtectedAgent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: "agent"}))
	r := httptest.NewRequest("POST", "/v1/agents/agent/execution-session", bytes.NewBufferString(`{"incarnation_id":"process"}`))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	require.Equal(t, 401, w.Code)
}
func TestGrantSessionAuthenticatedCAS(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(key))
	secret := "agent-secret"
	require.NoError(t, s.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: "agent", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	request := func(expected string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"incarnation_id": "process-" + expected, "expected_session": expected})
		r := httptest.NewRequest("POST", "/v1/agents/agent/execution-session", bytes.NewReader(raw))
		require.NoError(t, agent.SignRequest(r, "agent", secret, time.Now()))
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		return w
	}
	w := request("")
	require.Equal(t, 200, w.Code, w.Body.String())
	var session controlplane.AgentSession
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &session))
	w = request("wrong")
	require.Equal(t, 409, w.Code, w.Body.String())
	w = request(session.ID)
	require.Equal(t, 200, w.Code, w.Body.String())
}

func TestCommandGrantDerivesScopeAndRejectsRetiredCommand(t *testing.T) {
	s := guestExecutionTestServer(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(key))
	ctx := context.Background()
	owner, err := s.apiStore.GetAgent(ctx, "host-a")
	require.NoError(t, err)
	owner.AuthSecret = "secret"
	owner.SecretHash = agent.SecretHash(owner.AuthSecret)
	require.NoError(t, s.apiStore.SaveAgent(ctx, *owner))
	post := func(path string, v any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(v)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		r.Header.Set("X-Sysbox-Roles", "admin")
		require.NoError(t, agent.SignRequest(r, "host-a", "secret", time.Now()))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := post("/v1/agents/host-a/execution-session", map[string]string{"incarnation_id": "guest-process"})
	require.Equal(t, 200, w.Code, w.Body.String())
	var session controlplane.AgentSession
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &session))
	w = post("/v1/topologies/lab/nodes/web/executions", controlplane.GuestExecutionRequest{Argv: []string{"true"}})
	require.Equal(t, 202, w.Code, w.Body.String())
	commands, err := s.apiStore.ListAgentCommands(ctx, "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "host-a", commands[0].ID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	path := "/v1/agents/host-a/commands/" + cmd.ID + "/execution-grant"
	proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "guest-boot"}
	w = post(path, proof)
	require.Equal(t, 200, w.Code, w.Body.String())
	var signed controlplane.SignedExecutionGrant
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &signed))
	require.NoError(t, controlplane.VerifyExecutionGrant(pub, signed, "guest-boot", time.Now()))
	require.Equal(t, cmd.ResourceUID, signed.Grant.Scope.ResourceUID)
	require.Equal(t, cmd.Generation, signed.Grant.Scope.Generation)
	require.False(t, signed.Grant.ExpiresAt.After(cmd.LeaseUntil))
	proof.BootID = "rebooted-guest"
	w = post(path, proof)
	require.Equal(t, 409, w.Code, w.Body.String())
	proof.BootID = "guest-boot"
	w = post(path+"/authorize", ExecutionGrantAuthorization{Grant: signed, LeaseOwner: cmd.LeaseOwner})
	require.Equal(t, 200, w.Code, w.Body.String())
	w = post("/v1/agents/host-a/execution-grants/authorize", signed)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = post(path+"/renew", ExecutionGrantAuthorization{Grant: signed, LeaseOwner: cmd.LeaseOwner})
	require.Equal(t, 200, w.Code, w.Body.String())
	var renewed controlplane.SignedExecutionGrant
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &renewed))
	require.Equal(t, signed.Grant.Epoch, renewed.Grant.Epoch)
	require.True(t, renewed.Grant.ExpiresAt.After(signed.Grant.ExpiresAt))
	signed = renewed
	proof.Attempt++
	w = post(path, proof)
	require.Equal(t, 409, w.Code, w.Body.String())
	proof.Attempt--
	require.NoError(t, s.apiStore.DeleteAgentCommand(ctx, "host-a", cmd.ID))
	w = post("/v1/agents/host-a/execution-grants/inspect", signed.Grant.Scope)
	require.Equal(t, 200, w.Code, w.Body.String())
	var inspected controlplane.ExecutionGrant
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &inspected))
	require.Equal(t, signed.Grant.Epoch, inspected.Epoch)
	w = post("/v1/agents/host-a/execution-grants/revoke", signed.Grant)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = post("/v1/agents/host-a/execution-grants/authorize", signed)
	require.Equal(t, 409, w.Code, w.Body.String())
	w = post(path+"/authorize", ExecutionGrantAuthorization{Grant: signed, LeaseOwner: cmd.LeaseOwner})
	require.Equal(t, 409, w.Code, w.Body.String())
	w = post(path, proof)
	require.Equal(t, 409, w.Code, w.Body.String())
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"host-a": hostPub}
	stop, err := controlplane.SignExecutionTaskStopReceipt(key, signed.Grant, "host-boot", signed.Grant.TargetBootID, signed.Grant.ActionID, signed.Grant.Digest, controlplane.GuestTaskResult{Result: controlplane.GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}, "receipt", time.Now())
	require.NoError(t, err)
	w = post("/v1/hosts/host-a/execution-grants/stopped", stop)
	require.Equal(t, 401, w.Code, w.Body.String())
	stop, err = controlplane.SignExecutionTaskStopReceipt(hostKey, signed.Grant, "host-boot", signed.Grant.TargetBootID, signed.Grant.ActionID, signed.Grant.Digest, controlplane.GuestTaskResult{Result: controlplane.GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}, "receipt", time.Now())
	require.NoError(t, err)
	w = post("/v1/hosts/host-a/execution-grants/stopped", stop)
	require.Equal(t, 200, w.Code, w.Body.String())
	nextRequest := signed.Grant
	nextRequest.ActionID = "successor"
	nextRequest.ExpiresAt = time.Time{}
	next, err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).Issue(ctx, nextRequest, time.Minute)
	require.NoError(t, err)
	require.Greater(t, next.Epoch, signed.Grant.Epoch)
	w = post("/v1/hosts/host-a/execution-grants/stopped", stop)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).Authorize(ctx, *next))
}

func TestExecutionAuthorityBackendsSerializeCommandAndGrant(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		r := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		s, err := r.OpenSession(ctx, "agent", "boot", "")
		require.NoError(t, err)
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "test", Status: controlplane.AgentCommandStatusQueued}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		lease, ok, err := store.AcquireAgentCommandLease(ctx, "agent", "command", "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		storage := controlstore.AuthorityStorage{Store: store, CommandID: cmd.ID, Validate: func(record *persistence.CommandRecord) error {
			if record == nil || record.Retired || record.Command.Attempt != lease.Attempt {
				return persistence.ErrConflict
			}
			return nil
		}}
		r = persistence.NewExecutionGrantRepository(storage)
		g, err := r.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: s.ID, ActionID: "action", CommandID: cmd.ID, Attempt: uint64(lease.Attempt), Operation: "test", Digest: "digest"}, time.Minute)
		require.NoError(t, err)
		reopened := persistence.NewExecutionGrantRepository(storage)
		require.NoError(t, reopened.Authorize(ctx, *g))
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID))
		require.ErrorIs(t, reopened.Authorize(ctx, *g), persistence.ErrConflict)
		// Command retirement must not drop the authority epoch or falsely prove
		// its in-flight effects stopped. Takeover still requires a host receipt.
		base := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		replacement := *g
		replacement.ActionID = "next"
		_, err = base.Issue(ctx, replacement, time.Minute)
		require.ErrorIs(t, err, persistence.ErrConflict)
		require.NoError(t, base.Revoke(ctx, *g))
		require.NoError(t, base.AcknowledgeStop(ctx, *g, "host-stop"))
		next, err := base.Issue(ctx, replacement, time.Minute)
		require.NoError(t, err)
		require.Greater(t, next.Epoch, g.Epoch)
	})
}

func TestGrantScopeComesFromImmutableDispatchNotNestedStateLoad(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		s := &Server{apiStore: store}
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "guest_execution", Status: "running", TopologyUID: "topology-uid", ResourceUID: "node-uid", Generation: 2, ActionID: "action", LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Minute), Execution: &controlplane.GuestExecution{ID: "operation", Topology: "lab", Node: "web"}, ExecutionRequest: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
		require.NoError(t, store.SaveAgentCommand(context.Background(), cmd))
		r := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store, CommandID: cmd.ID, Validate: func(record *persistence.CommandRecord) error {
			g, err := s.sourceGrant(context.Background(), "agent", ExecutionGrantRequest{SessionID: "session", Attempt: 1, LeaseOwner: "worker"}, record)
			if err == nil {
				require.Equal(t, "topology-uid", g.Scope.TopologyUID)
				require.Equal(t, "node-uid", g.Scope.ResourceUID)
			}
			return err
		}})
		_, err := r.OpenSession(context.Background(), "agent", "incarnation", "")
		require.NoError(t, err)
	})
}

func TestRunGrantDerivesGuestGrantThroughDurableAuthority(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	issuerPub, issuerKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuerKey))
	secret := "derive-secret"
	require.NoError(t, s.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: "agent", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	ctx := context.Background()
	repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	session, err := repo.OpenSession(ctx, "agent", "host-process", "")
	require.NoError(t, err)
	run := controlplane.Run{ID: "run", Topology: "lab", Revision: "revision", Op: "apply", Status: controlplane.RunAssigned, AgentID: "agent", Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(2 * time.Minute)}
	command := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusLeased, Run: &run, TopologyUID: grantTestTopologyUID(t, s, "lab"), ActionID: "run", Attempt: 1, LeaseOwner: "worker", LeaseUntil: run.LeaseUntil}
	require.NoError(t, s.apiStore.SaveAgentCommand(ctx, command))
	source, err := s.sourceGrant(ctx, "agent", ExecutionGrantRequest{SessionID: session.ID, Attempt: 1, LeaseOwner: "worker", BootID: "host"}, &persistence.CommandRecord{Command: command})
	require.NoError(t, err)
	parent, err := repo.Issue(ctx, source, time.Minute)
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(issuerKey, *parent, "host", time.Now())
	require.NoError(t, err)
	hostPublic, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"agent": hostPublic}
	proof, err := controlplane.SignGuestGrantDerivation(hostKey, controlplane.GuestGrantDerivation{Parent: signed, TaskID: "task", TaskDigest: "task-digest", ResourceUID: "resource", Generation: 1, GuestBootID: "guest"})
	require.NoError(t, err)
	body, err := json.Marshal(proof)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/agent/execution-grant/derive-guest", bytes.NewReader(body))
	require.NoError(t, agent.SignRequest(req, "agent", secret, time.Now()))
	response := httptest.NewRecorder()
	s.ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var child controlplane.SignedExecutionGrant
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &child))
	require.NoError(t, controlplane.VerifyExecutionGrant(issuerPub, child, "guest", time.Now()))
	require.Equal(t, "guest_task", child.Grant.Operation)
	require.Equal(t, parent.CommandID, child.Grant.CommandID)
	stored, err := repo.InspectGrant(ctx, "agent", child.Grant.Scope)
	require.NoError(t, err)
	require.Equal(t, child.Grant.Epoch, stored.Epoch)
}

func TestGCGrantBindsCandidateAndSession(t *testing.T) {
	s := guestExecutionTestServer(t)
	_, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
	secret := "gc-secret"
	require.NoError(t, s.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: "host-a", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	ctx := context.Background()
	session, err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).OpenSession(ctx, "host-a", "guest-process", "")
	require.NoError(t, err)
	manager, err := s.stateManager("lab")
	require.NoError(t, err)
	canonical, err := manager.LoadWithContext(ctx)
	require.NoError(t, err)
	lifecycle, err := s.actionRepository("lab")
	require.NoError(t, err)
	inventory, err := lifecycle.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	candidate := hostsupervisor.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: canonical.TopologyUID, OwnerUID: canonical.TopologyUID, ResourceUID: "a89db8cb2f24c80976a32d737eb3f3485e39245f14e08a10cd3fdc9135d4c011", Generation: 1, InventoryDigest: inventory.Digest}
	require.NoError(t, s.reserveGCWithAuthority(ctx, "lab", persistence.GCReservation{ID: "gc-command", AgentID: "host-a", Candidate: candidate}))
	require.NoError(t, s.publishGCReservation(ctx, "lab", persistence.GCReservation{ID: "gc-command", AgentID: "host-a", Candidate: candidate}))
	lease, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "host-a", "gc-command", "gc-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, lease.Attempt)
	body, _ := json.Marshal(map[string]any{"candidate": candidate, "topology": "lab", "session_id": session.ID, "action_id": "gc-command", "command_id": "gc-command", "attempt": 1, "lease_owner": "gc-worker", "boot_id": "host"})
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/gc-grant", bytes.NewReader(body))
	require.NoError(t, agent.SignRequest(req, "host-a", secret, time.Now()))
	response := httptest.NewRecorder()
	s.ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var signed controlplane.SignedExecutionGrant
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &signed))
	require.Equal(t, "gc_delete", signed.Grant.Operation)
	require.Equal(t, candidate.ResourceUID, signed.Grant.Scope.ResourceUID)
	authorizeReq := httptest.NewRequest(http.MethodPost, "/v1/agents/host-a/execution-grants/authorize", bytes.NewReader(response.Body.Bytes()))
	require.NoError(t, agent.SignRequest(authorizeReq, "host-a", secret, time.Now()))
	authorizeResp := httptest.NewRecorder()
	s.ServeHTTP(authorizeResp, authorizeReq)
	require.Equal(t, http.StatusOK, authorizeResp.Code, authorizeResp.Body.String())
}
