package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestHostRunCompletionRecoversRetiredCommandAndExpiredLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		now := time.Now().UTC()
		run := controlplane.Run{ID: "host_run", Topology: "lab", Op: "apply", Operation: "apply", Revision: "revision", AgentID: "agent", Status: controlplane.RunRunning, Attempt: 1, LeaseOwner: "old-worker", QueuedAt: now.Add(-time.Hour), LeaseUntil: now.Add(-time.Minute)}
		require.NoError(t, store.SaveRun(ctx, run))
		command := controlplane.AgentCommand{ID: "host-command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusDelivered, Run: &run, TopologyUID: "topology", ActionID: "action", Attempt: 1, LeaseOwner: "old-worker", LeaseUntil: run.LeaseUntil}
		require.NoError(t, store.SaveAgentCommand(ctx, command))
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "old-session", "")
		require.NoError(t, err)
		req := hostsupervisor.RunRequest{RunID: run.ID, Topology: run.Topology, TopologyUID: command.TopologyUID, Revision: run.Revision, ActionID: command.ActionID, Action: "apply"}
		digest, err := req.Digest()
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: command.ID, ActionID: command.ActionID, Attempt: 1, Operation: "run_apply", Digest: digest, TargetBootID: "host", Scope: controlplane.ExecutionScope{TopologyUID: "topology"}}, time.Minute)
		require.NoError(t, err)
		observed := run
		observed.Status = controlplane.RunDone
		observed.EndedAt = now
		evidence, err := json.Marshal(observed)
		require.NoError(t, err)
		receipt, err := controlplane.SignRunStopReceipt(key, *grant, "host", "receipt", evidence, now)
		require.NoError(t, err)
		post := func(r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) *httptest.ResponseRecorder {
			body, err := json.Marshal(hostRunCompletion{Receipt: r, Evidence: raw})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			s.mux.ServeHTTP(response, httptest.NewRequest("POST", "/v1/hosts/agent/runs/host_run/completed", bytes.NewReader(body)))
			return response
		}
		altered := observed
		altered.Err = "forged"
		raw, _ := json.Marshal(altered)
		tamperedResponse := post(receipt, raw)
		require.Equal(t, 409, tamperedResponse.Code, tamperedResponse.Body.String())
		for _, mutate := range []func(*controlplane.Run){
			func(r *controlplane.Run) { r.ID = "other" },
			func(r *controlplane.Run) { r.QueuedAt = r.QueuedAt.Add(time.Second) },
			func(r *controlplane.Run) { r.Revision = "other" },
			func(r *controlplane.Run) { r.Target = "other" },
			func(r *controlplane.Run) { r.Attempt++ },
			func(r *controlplane.Run) { r.LeaseOwner = "different-owner" },
		} {
			changed := observed
			mutate(&changed)
			raw, _ := json.Marshal(changed)
			wrong, err := controlplane.SignRunStopReceipt(key, *grant, "host", "wrong", raw, now)
			require.NoError(t, err)
			require.Equal(t, 409, post(wrong, raw).Code)
		}
		generic, err := controlplane.SignExecutionStopReceipt(key, *grant, "host", "generic", now)
		require.NoError(t, err)
		require.Equal(t, 409, post(generic, evidence).Code)
		stopBody, _ := json.Marshal(receipt)
		stop := httptest.NewRecorder()
		s.mux.ServeHTTP(stop, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(stopBody)))
		require.Equal(t, 409, stop.Code)
		replacement, acquired, err := store.AcquireAgentCommandLease(ctx, "agent", command.ID, "replacement-worker", time.Minute)
		require.NoError(t, err)
		require.True(t, acquired)
		require.Greater(t, replacement.Attempt, command.Attempt)
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", command.ID))
		_, expired, err := store.ExpireRunLease(ctx, run.ID, now)
		require.NoError(t, err)
		require.True(t, expired)
		_, err = authority.OpenSession(ctx, "agent", "replacement", session.ID)
		require.NoError(t, err)
		response := post(receipt, evidence)
		require.Equal(t, 200, response.Code, response.Body.String())
		done, err := store.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunDone, done.Status)
		require.Equal(t, "old-worker", done.LeaseOwner)
		nextSession, err := authority.OpenSession(ctx, "agent", "replacement", session.ID)
		require.NoError(t, err)
		next := *grant
		next.SessionID = nextSession.ID
		next.CommandID = "successor"
		next.ActionID = "successor"
		successor, err := authority.Issue(ctx, next, time.Minute)
		require.NoError(t, err)
		response = post(receipt, evidence)
		require.Equal(t, 200, response.Code, response.Body.String())
		require.NoError(t, authority.Authorize(ctx, *successor))

		// Simulate a controller admitting a newer actual execution attempt.
		// A historical command grant must never overwrite its canonical owner.
		var records persistence.RunStorage
		switch backend := store.(type) {
		case *localAPIStore:
			records = localRunStorage{store: backend}
		case *sqliteAPIStore:
			records = sqliteRunStorage{store: backend}
		case *postgresAPIStore:
			records = postgresRunStorage{store: backend}
		}
		_, err = records.UpdateRun(ctx, run.ID, func(current *controlplane.Run) (*controlplane.Run, error) {
			current.Status = controlplane.RunRunning
			current.Attempt++
			current.LeaseOwner = "new-run-owner"
			current.LeaseUntil = now.Add(time.Minute)
			current.StartedAt = now.Add(time.Second)
			current.EndedAt = time.Time{}
			return current, nil
		})
		require.NoError(t, err)
		response = post(receipt, evidence)
		require.Equal(t, 409, response.Code, response.Body.String())
		newRun, err := store.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.Equal(t, "new-run-owner", newRun.LeaseOwner)
		require.Equal(t, controlplane.RunRunning, newRun.Status)
		require.NoError(t, authority.Authorize(ctx, *successor))
	})
}
