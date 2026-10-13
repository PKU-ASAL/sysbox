package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestHostRunCompletionRecoversRetiredCommandAndExpiredLease(t *testing.T) {
	for _, action := range []string{"apply", "destroy", "reset"} {
		t.Run(action, func(t *testing.T) { hostRunCompletionRecoversRetiredCommand(t, action) })
	}
}

func hostRunCompletionRecoversRetiredCommand(t *testing.T, action string) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		lifecycle, err := s.actionRepository("lab")
		require.NoError(t, err)
		initial, err := lifecycle.LoadState(ctx, "lab")
		require.NoError(t, err)
		owner := initial.TopologyUID
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		now := time.Now().UTC()
		run := controlplane.Run{ID: "host_run", Topology: "lab", Op: action, Operation: action, Revision: "revision", AgentID: "agent", Status: controlplane.RunRunning, Attempt: 1, LeaseOwner: "old-worker", QueuedAt: now.Add(-time.Hour), LeaseUntil: now.Add(-time.Minute)}
		if action == "reset" {
			run.Target = "sysbox_node.web"
		}
		require.NoError(t, store.SaveRun(ctx, run))
		command := controlplane.AgentCommand{ID: "host-command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusDelivered, Run: &run, TopologyUID: owner, ActionID: "action", Attempt: 1, LeaseOwner: "old-worker", LeaseUntil: run.LeaseUntil}
		if run.Target != "" {
			command.ResourceUID, command.Generation = "node", 1
		}
		require.NoError(t, store.SaveAgentCommand(ctx, command))
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "old-session", "")
		require.NoError(t, err)
		req := hostsupervisor.RunRequest{RunID: run.ID, Topology: run.Topology, TopologyUID: command.TopologyUID, Revision: run.Revision, ActionID: command.ActionID, Action: action, Target: run.Target, ResourceUID: command.ResourceUID, Generation: command.Generation}
		digest, err := req.Digest()
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: command.ID, ActionID: command.ActionID, Attempt: 1, Operation: "run_" + action, Digest: digest, TargetBootID: "host", Scope: controlplane.ExecutionScope{TopologyUID: owner}}, time.Minute)
		require.NoError(t, err)
		observed := run
		observed.Status = controlplane.RunDone
		if action == "reset" {
			observed.Status, observed.Recoverable, observed.Err = controlplane.RunFailed, true, "reset interrupted after safe checkpoint"
		}
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
		child, err := authority.DeriveGuestTask(ctx, *grant, controlplane.ExecutionGrant{
			AgentID: grant.AgentID, SessionID: grant.SessionID, CommandID: grant.CommandID,
			Attempt: grant.Attempt, ActionID: "child", Operation: "guest_task", Digest: "child-digest",
			TargetBootID: "guest", Scope: controlplane.ExecutionScope{TopologyUID: owner, ResourceUID: "node", Generation: 1},
		}, time.Minute)
		require.NoError(t, err)
		require.Equal(t, 409, post(receipt, evidence).Code, "a live child must block canonical Run completion")
		beforeStop, err := store.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunRunning, beforeStop.Status)
		require.NoError(t, authority.AcceptHostStop(ctx, *child, "verified-child-stop"))
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
		if action != "destroy" {
			require.NoError(t, store.DeleteAgentCommand(ctx, "agent", command.ID))
		}
		_, expired, err := store.ExpireRunLease(ctx, run.ID, now)
		require.NoError(t, err)
		require.True(t, expired)
		_, err = authority.OpenSession(ctx, "agent", "replacement", session.ID)
		require.NoError(t, err)
		response := post(receipt, evidence)
		require.Equal(t, 200, response.Code, response.Body.String())
		pending, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, pending, "authenticated host completion must retire dispatch without a worker acknowledgment")
		done, err := store.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.Equal(t, observed.Status, done.Status)
		require.Equal(t, "old-worker", done.LeaseOwner)
		require.NotNil(t, done.HostCompletion, "canonical terminal result must retain signed host evidence")
		require.Equal(t, receipt, done.HostCompletion.Receipt)
		require.NoError(t, controlplane.VerifyRunStopReceipt(pub, done.HostCompletion.Receipt, done.HostCompletion.Evidence))
		stripped := *done
		stripped.HostCompletion = nil
		require.ErrorIs(t, store.SaveRun(ctx, stripped), persistence.ErrConflict, "ordinary save cannot discard stop evidence")
		forged := *done
		forged.ID = "injected-proof"
		require.ErrorIs(t, store.SaveRun(ctx, forged), persistence.ErrConflict, "ordinary save cannot mint host evidence")
		if action == "reset" {
			bridge := &ExecutionBridge{server: s}
			hostBridge := &HostExecutionBridge{ExecutionBridge: bridge}
			require.NoError(t, hostBridge.BindHostIdentity("agent", pub))
			require.NoError(t, hostBridge.BindHostIdentity("agent", pub))
			foreign, _, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			require.Error(t, hostBridge.BindHostIdentity("agent", foreign))
			require.Error(t, hostBridge.BindHostIdentity("other", pub))
			continuation := &controlplane.Run{ID: "continuation", Topology: "lab", Op: "reset", ParentID: done.ID}
			require.NoError(t, hostBridge.VerifyResetContinuation(ctx, continuation, done))
			stale := *done
			stale.QueuedAt = stale.QueuedAt.Add(time.Second)
			require.Error(t, bridge.VerifyResetContinuation(ctx, continuation, &stale))
			assertResetActionRunMapping(t, s, done)
		}
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
		if action == "reset" {
			bridge := &ExecutionBridge{server: s}
			require.NoError(t, bridge.VerifyResetContinuation(ctx, &controlplane.Run{ID: "continuation", Topology: "lab", Op: "reset", ParentID: done.ID}, done), "historical stop remains verifiable after successor admission")
		}

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

func assertResetActionRunMapping(t *testing.T, s *Server, parent *controlplane.Run) {
	t.Helper()
	ctx := context.Background()
	bridge := &ExecutionBridge{server: s}
	child := controlplane.Run{ID: "mapped-continuation", Topology: parent.Topology, Op: "reset", ParentID: parent.ID, Revision: parent.Revision, AgentID: parent.AgentID, QueuedAt: time.Now().UTC(), Status: controlplane.RunFailed, EndedAt: time.Now().UTC()}
	require.NoError(t, s.apiStore.SaveRun(ctx, child))
	_, err := bridge.ActionRun(ctx, parent.Topology, child.ID)
	require.Error(t, err, "ParentID and signed stop alone cannot replace continuation admission")
	repo, err := s.actionRepository(parent.Topology)
	require.NoError(t, err)
	mgr := state.NewManagerWithBackend(repo.StateBackend(parent.Topology))
	st, err := mgr.Load()
	require.NoError(t, err)
	res := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: st.TopologyUID, ResourceUID: "web", Generation: 1, ExternalID: "old-web"}
	st.Resources = append(st.Resources, res)
	require.NoError(t, mgr.Save(st))
	a := runtime.ActionRecord{ID: "original-reset-action", RunID: parent.ID, Topology: parent.Topology, RunQueuedAt: parent.QueuedAt, RunRevision: parent.Revision, Resource: res.Address, OwnerUID: res.OwnerUID, ResourceUID: res.ResourceUID, Generation: 2, Action: controlplane.PlanActionReset, Status: runtime.ActionIntentCommitted, Phase: "accepted", StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Candidate: &res}
	require.NoError(t, repo.CommitResetIntents(ctx, []runtime.ActionRecord{a}))
	a.Revision = 1
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	recorder := runtime.NewFileRecorder(path, parent.ID, parent.Topology)
	recorder.SetStateSerialBefore(st.Meta.Serial)
	require.NoError(t, recorder.Begin("reset", &runtime.Plan{Actions: []controlplane.PlannedChange{{Address: res.Address, Action: controlplane.PlanActionReset}}}))
	cp, err := runtime.LoadCheckpointFile(path)
	require.NoError(t, err)
	_, err = repo.CommitResetContinuation(ctx, runtime.ResetContinuationRequest{ID: child.ID, TopologyUID: st.TopologyUID, RunQueuedAt: parent.QueuedAt, RunRevision: parent.Revision, ExpectedSerial: st.Meta.Serial, Checkpoint: *cp, Existing: []runtime.ActionRecord{a}})
	require.NoError(t, err)
	owner, err := bridge.ActionRun(ctx, parent.Topology, child.ID)
	require.NoError(t, err)
	require.Equal(t, parent.ID, owner)
	second := child
	second.ID, second.ParentID = "second-continuation", child.ID
	require.NoError(t, s.apiStore.SaveRun(ctx, second))
	_, err = bridge.ActionRun(ctx, parent.Topology, second.ID)
	require.ErrorContains(t, err, "host completion evidence", "root proof must not bypass a missing intermediate stop")
	_, err = bridge.ActionRun(ctx, "foreign-topology", child.ID)
	require.Error(t, err)
	child.ID, child.Revision = "wrong-revision", "different"
	require.NoError(t, s.apiStore.SaveRun(ctx, child))
	_, err = bridge.ActionRun(ctx, parent.Topology, child.ID)
	require.Error(t, err)
}
