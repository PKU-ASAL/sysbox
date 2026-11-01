package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func hostCompletionRunningRun(t *testing.T, store controlstore.Store, id string) controlplane.Run {
	t.Helper()
	run := controlplane.Run{ID: id, Topology: "lab", Op: "apply", Operation: "apply", Revision: "revision", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 1, QueuedAt: time.Now().Add(-time.Hour).UTC(), StartedAt: time.Now().Add(-time.Minute).UTC(), LeaseUntil: time.Now().Add(time.Hour).UTC()}
	require.NoError(t, store.SaveRun(context.Background(), run))
	return run
}
func (f *hostRunFixture) publishObservation(t *testing.T, serial int64) controlplane.ResourceProjection {
	t.Helper()
	require.NoError(t, f.server.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: f.run.AgentID}))
	now := time.Now().UTC()
	projection := controlplane.ResourceProjection{AgentID: f.run.AgentID, SessionID: f.grant.SessionID, Sequence: 1, Topology: f.run.Topology, Workspace: f.run.Workspace, Serial: serial, ObservedAt: now, Health: model.TopologyHealth{Status: model.ResourceHealthHealthy}}
	inv := controlplane.AgentInventory{AgentID: f.run.AgentID, SessionID: f.grant.SessionID, Sequence: 1, ObservedAt: now, Topologies: []controlplane.InventoryItem{{Topology: f.run.Topology, Workspace: f.run.Workspace, Serial: serial, Health: "healthy", ResourceCount: 2}}}
	o, err := controlplane.SignHostObservation(f.key, controlplane.HostObservation{AgentID: f.run.AgentID, SessionID: f.grant.SessionID, Sequence: 1, BootID: "test-host", ObservedAt: now, Inventory: inv, Projections: []controlplane.ResourceProjection{projection}})
	require.NoError(t, err)
	raw, err := json.Marshal(o)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	f.server.mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/agents/"+f.run.AgentID+"/observations", bytes.NewReader(raw)))
	require.Equal(t, 202, w.Code, w.Body.String())
	return projection
}
func TestHostRunCommitFailureKeepsGrantAndObservation(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "api.db")
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	run := hostCompletionRunningRun(t, s.apiStore, "commit-failure")
	f := prepareHostRunFixture(t, s, run, "owner")
	observation := f.publishObservation(t, 99)
	db, err := s.apiStore.(*controlstore.SQLite).OpenDB()
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER reject_host_result BEFORE INSERT ON sysbox_runs WHEN NEW.status='done' BEGIN SELECT RAISE(ABORT,'injected host result write failure'); END`)
	require.NoError(t, err)
	result := run
	result.MarkFinished(nil, time.Now().UTC())
	response := f.post(t, result)
	require.Equal(t, 500, response.Code, response.Body.String())
	current, err := s.apiStore.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunRunning, current.Status)
	require.Nil(t, current.HostCompletion)
	cached, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunRunning, cached.Status)
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	grant, err := authority.InspectGrant(ctx, run.AgentID, f.grant.Scope)
	require.NoError(t, err)
	require.NotEqual(t, controlplane.GrantStopped, grant.State)
	require.Empty(t, grant.StopReceipt)
	command, err := readFailureCommand(ctx, s.apiStore, f.grant.CommandID)
	require.NoError(t, err)
	require.False(t, command.Retired)
	projected, err := s.apiStore.LoadResourceProjection(ctx, run.Topology)
	require.NoError(t, err)
	require.Equal(t, observation, *projected)
	_, err = db.Exec(`DROP TRIGGER reject_host_result`)
	require.NoError(t, err)
	require.NotNil(t, f.complete(t, result).HostCompletion, "the identical signed receipt recovers after persistence is repaired")
}
func TestHostRunCompletionReplayPreservesSignedObservationAcrossRestart(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		run := hostCompletionRunningRun(t, store, "replay")
		f := prepareHostRunFixture(t, s, run, "owner")
		result := run
		result.MarkFinished(nil, time.Now().UTC())
		f.complete(t, result)
		observation := f.publishObservation(t, 99)
		f.complete(t, result)
		require.Equal(t, observation, s.agents.ListResourceProjections("lab")[0])
		cold := stateDeleteTestServer(t, store)
		got, err := cold.apiStore.LoadResourceProjection(ctx, "lab")
		require.NoError(t, err)
		require.Equal(t, observation, *got)
		// Public summary reads the same durable observation, without a completion cache.
		response := httptest.NewRecorder()
		cold.mux.ServeHTTP(response, httptest.NewRequest("GET", "/v1/agents/agent/projections", nil))
		require.Equal(t, 200, response.Code, response.Body.String())
		var body struct {
			Projections []controlplane.Projection `json:"projections"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		require.Len(t, body.Projections, 1)
		require.EqualValues(t, 99, body.Projections[0].Serial)
		require.EqualValues(t, 2, body.Projections[0].ResourceCount)
		inventory, err := store.GetAgentInventory(ctx, run.AgentID)
		require.NoError(t, err)
		inventory.Sequence++
		inventory.ObservedAt = time.Now().Add(-2 * time.Minute)
		require.NoError(t, store.SaveAgentInventory(ctx, *inventory))
		response = httptest.NewRecorder()
		cold.mux.ServeHTTP(response, httptest.NewRequest("GET", "/v1/agents/agent/projections", nil))
		require.Equal(t, 200, response.Code, response.Body.String())
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		require.Equal(t, "unknown", body.Projections[0].Health)
	})
}
func TestHostRunResultKeepsAssertionAndClearsRenewedLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		run := hostCompletionRunningRun(t, store, "renewed")
		f := prepareHostRunFixture(t, stateDeleteTestServer(t, store), run, "owner")
		_, ok, err := store.RenewRunLease(context.Background(), run.ID, run.AgentID, run.LeaseOwner, 2*time.Hour)
		require.NoError(t, err)
		require.True(t, ok)
		result := run
		result.Assertion = &controlplane.AssertionResult{}
		result.MarkFinished(nil, time.Now().UTC())
		changed := result
		changed.QueuedAt = result.QueuedAt.Add(-time.Hour)
		require.Equal(t, 409, f.post(t, changed).Code)
		done := f.complete(t, result)
		require.Equal(t, result.Assertion, done.Assertion)
		require.True(t, done.LeaseUntil.IsZero(), "confirmed host stop must clear execution lease")
		require.Equal(t, *done, *f.complete(t, result))
		changed = result
		changed.Status = controlplane.RunFailed
		require.Equal(t, 409, f.post(t, changed).Code)
	})
}
