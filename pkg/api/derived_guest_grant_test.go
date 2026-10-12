package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestDerivedGuestRenewalAuthority(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(key))
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
	repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := repo.OpenSession(ctx, "agent", "worker-process", "")
	require.NoError(t, err)
	run := controlplane.Run{ID: "run", Topology: "lab", Revision: "revision", Op: "apply"}
	cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusLeased, Run: &run, TopologyUID: grantTestTopologyUID(t, s, "lab"), ActionID: "run", Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(4 * time.Minute)}
	require.NoError(t, s.apiStore.SaveAgentCommand(ctx, cmd))
	g, err := s.sourceGrant(ctx, "agent", ExecutionGrantRequest{SessionID: session.ID, Attempt: 1, LeaseOwner: "worker", BootID: "host"}, &persistence.CommandRecord{Command: cmd})
	require.NoError(t, err)
	g.ExpiresAt = time.Now().Add(time.Minute)
	p, err := repo.Issue(ctx, g, time.Minute)
	require.NoError(t, err)
	sign := func(g controlplane.ExecutionGrant) controlplane.SignedExecutionGrant {
		signed, e := controlplane.SignExecutionGrant(key, g, g.TargetBootID, time.Now())
		require.NoError(t, e)
		return signed
	}
	parent := sign(*p)
	post := func(route string, body any, authenticate bool) *httptest.ResponseRecorder {
		raw, e := json.Marshal(body)
		require.NoError(t, e)
		r := httptest.NewRequest(http.MethodPost, "/v1/agents/agent/execution-grant/"+route, bytes.NewReader(raw))
		if authenticate {
			require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	derive := map[string]any{"parent": parent, "task_id": "task", "task_digest": "digest", "resource_uid": "node", "generation": 1, "guest_boot_id": "guest"}
	w := post("derive-guest", derive, true)
	require.Equal(t, http.StatusUnauthorized, w.Code, "Agent credentials alone must not authorize a host-selected task: %s", w.Body.String())
	hostPublic, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"agent": hostPublic}
	proof, err := controlplane.SignGuestGrantDerivation(hostKey, controlplane.GuestGrantDerivation{Parent: parent, TaskID: "task", TaskDigest: "digest", ResourceUID: "node", Generation: 1, GuestBootID: "guest"})
	require.NoError(t, err)
	for _, field := range []string{"task", "digest", "resource", "generation", "boot", "parent", "signature"} {
		t.Run("host-proof/"+field, func(t *testing.T) {
			changed := proof
			switch field {
			case "task":
				changed.TaskID = "other"
			case "digest":
				changed.TaskDigest = "other"
			case "resource":
				changed.ResourceUID = "other"
			case "generation":
				changed.Generation++
			case "boot":
				changed.GuestBootID = "other"
			case "parent":
				changed.Parent.Grant.Epoch++
			case "signature":
				changed.HostSignature = nil
			}
			w := post("derive-guest", changed, true)
			require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		})
	}
	w = post("derive-guest", proof, true)
	require.Equal(t, 200, w.Code, w.Body.String())
	var child controlplane.SignedExecutionGrant
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &child))
	authorize := func() *httptest.ResponseRecorder {
		raw, err := json.Marshal(child)
		require.NoError(t, err)
		r := httptest.NewRequest(http.MethodPost, "/v1/agents/agent/execution-grants/authorize", bytes.NewReader(raw))
		require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w = authorize()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = post("derive-guest", proof, true)
	require.Equal(t, 200, w.Code, w.Body.String())
	var retry controlplane.SignedExecutionGrant
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &retry))
	require.Equal(t, child.Grant, retry.Grant)
	// A newer ledger parent deadline cannot extend a child presented with the old signed parent.
	later, err := repo.Renew(ctx, *p, 2*time.Minute)
	require.NoError(t, err)
	require.True(t, later.ExpiresAt.After(parent.Grant.ExpiresAt))
	body := map[string]any{"parent": parent, "child": child}
	w = post("renew-guest", body, false)
	require.Equal(t, 401, w.Code)
	w = post("renew-guest", body, true)
	require.Equal(t, 200, w.Code, w.Body.String())
	var renewed controlplane.SignedExecutionGrant
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &renewed))
	require.NoError(t, controlplane.VerifyExecutionGrant(pub, renewed, "guest", time.Now()))
	require.Equal(t, child.Grant.Epoch, renewed.Grant.Epoch)
	require.False(t, renewed.Grant.ExpiresAt.After(parent.Grant.ExpiresAt))
	stored, err := repo.InspectGrant(ctx, "agent", parent.Grant.Scope)
	require.NoError(t, err)
	require.Equal(t, *later, *stored)
	for _, field := range []string{"session", "command", "parent_epoch", "wrong_parent_epoch", "wrong_parent_session", "signature", "expired_parent"} {
		t.Run(field, func(t *testing.T) {
			badChild, badParent := child, parent
			switch field {
			case "session":
				badChild.Grant.SessionID = "other"
				badChild = sign(badChild.Grant)
			case "command":
				badChild.Grant.CommandID = "other"
				badChild = sign(badChild.Grant)
			case "parent_epoch":
				badChild.Grant.ParentEpoch++
				badChild = sign(badChild.Grant)
			case "wrong_parent_epoch":
				badParent.Grant.Epoch++
				badParent = sign(badParent.Grant)
			case "wrong_parent_session":
				badParent.Grant.SessionID = "other"
				badParent = sign(badParent.Grant)
			case "signature":
				badChild.Signature = append([]byte(nil), badChild.Signature...)
				badChild.Signature[0] ^= 1
			case "expired_parent":
				badParent.Grant.ExpiresAt = time.Now().Add(-time.Second)
				var err error
				badParent, err = controlplane.SignExecutionGrant(key, badParent.Grant, "host", time.Now().Add(-time.Minute))
				require.NoError(t, err)
			}
			w := post("renew-guest", map[string]any{"parent": badParent, "child": badChild}, true)
			require.NotEqual(t, 200, w.Code)
		})
	}
	require.NoError(t, repo.Revoke(ctx, *later))
	w = authorize()
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.NoError(t, s.apiStore.DeleteAgentCommand(ctx, "agent", cmd.ID))
	require.ErrorIs(t, s.derivedGuestStorage(ctx, parent).validate(&persistence.CommandRecord{Command: cmd, Retired: true}), persistence.ErrConflict)
	w = post("renew-guest", body, true)
	require.NotEqual(t, 200, w.Code)
	w = post("derive-guest", proof, true)
	require.NotEqual(t, 200, w.Code)
	w = authorize()
	require.NotEqual(t, 200, w.Code)
}

func TestRejectedDerivedGrantPersistsClockFloor(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		storage := apiExecutionGrantStorage{store: store}
		repo := persistence.NewExecutionGrantRepository(storage)
		session, err := repo.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		parent := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: session.ID, ActionID: "run", CommandID: "command", Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "host"}
		child := parent
		child.Scope.ResourceUID, child.Scope.Generation = "node", 1
		child.ActionID, child.Operation, child.TargetBootID = "task", "guest_task", "guest"
		before := time.Now().UTC()
		_, err = mustDeriveGuestGrant(ctx, storage, parent, child, time.Minute)
		require.ErrorIs(t, err, persistence.ErrConflict)
		raw, err := storage.ReadExecutionAuthority(ctx)
		require.NoError(t, err)
		var ledger struct {
			ObservedAt time.Time `json:"observed_at"`
		}
		require.NoError(t, json.Unmarshal(raw, &ledger))
		require.False(t, ledger.ObservedAt.Before(before), "rejected transition must commit its clock floor")
	})
}

func TestRejectedDerivedEnvelopePersistsClockFloor(t *testing.T) {
	for _, route := range []string{"derive-guest", "renew-guest"} {
		t.Run(route, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			_, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			require.NoError(t, s.ConfigureExecutionGrantSigner(key))
			ctx := context.Background()
			require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "agent", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
			body := map[string]any{"parent": controlplane.SignedExecutionGrant{}}
			if route == "derive-guest" {
				body["task_id"], body["task_digest"], body["resource_uid"], body["generation"], body["guest_boot_id"] = "task", "digest", "node", 1, "guest"
			} else {
				body["child"] = controlplane.SignedExecutionGrant{}
			}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			r := httptest.NewRequest(http.MethodPost, "/v1/agents/agent/execution-grant/"+route, bytes.NewReader(raw))
			require.NoError(t, agent.SignRequest(r, "agent", "secret", time.Now()))
			before := time.Now().UTC()
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			want := http.StatusConflict
			if route == "derive-guest" {
				want = http.StatusUnauthorized
			}
			require.Equal(t, want, w.Code)
			raw, err = (apiExecutionGrantStorage{store: s.apiStore}).ReadExecutionAuthority(ctx)
			require.NoError(t, err)
			var ledger struct {
				ObservedAt time.Time `json:"observed_at"`
			}
			require.NoError(t, json.Unmarshal(raw, &ledger))
			require.False(t, ledger.ObservedAt.Before(before))
		})
	}
}
