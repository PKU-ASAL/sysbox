package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func observationTestSession(t *testing.T, store apiStore, agent string) string {
	t.Helper()
	s, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).OpenSession(context.Background(), agent, "test-process", "")
	require.NoError(t, err)
	return s.ID
}
func bindObservationTestBody(t *testing.T, s *Server, agent string, body *bytes.Buffer) *bytes.Buffer {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal(body.Bytes(), &value))
	value["session_id"] = observationTestSession(t, s.apiStore, agent)
	value["sequence"] = 1
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return bytes.NewBuffer(raw)
}

func TestObservationStoresRejectOldSessionAndSequence(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		s, err := repo.OpenSession(ctx, "agent", "one", "")
		require.NoError(t, err)
		inv := controlplane.AgentInventory{AgentID: "agent", SessionID: s.ID, Sequence: 2, ObservedAt: time.Now()}
		require.NoError(t, store.SaveAgentInventory(ctx, inv))
		inv.Sequence = 1
		inv.ObservedAt = inv.ObservedAt.Add(time.Hour)
		require.ErrorIs(t, store.SaveAgentInventory(ctx, inv), persistence.ErrConflict)
		projection := controlplane.ResourceProjection{AgentID: "agent", SessionID: s.ID, Sequence: 2, Topology: "lab"}
		require.NoError(t, store.SaveResourceProjection(ctx, projection))
		projection.Sequence = 1
		require.ErrorIs(t, store.SaveResourceProjection(ctx, projection), persistence.ErrConflict)
		next, err := repo.OpenSession(ctx, "agent", "two", s.ID)
		require.NoError(t, err)
		inv.Sequence = 3
		require.ErrorIs(t, store.SaveAgentInventory(ctx, inv), persistence.ErrConflict)
		_, err = store.GetAgentInventory(ctx, "agent")
		require.ErrorIs(t, err, persistence.ErrConflict)
		inv.SessionID = next.ID
		inv.Sequence = 1
		require.NoError(t, store.SaveAgentInventory(ctx, inv))
		got, err := store.GetAgentInventory(ctx, "agent")
		require.NoError(t, err)
		require.Equal(t, next.ID, got.SessionID)
	})
}

func TestProjectionHTTPRejectsStaleSequenceWithoutPublishingCache(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: "agent"}))
	projection := controlplane.ResourceProjection{AgentID: "agent", SessionID: observationTestSession(t, s.apiStore, "agent"), Sequence: 2, Topology: "lab", ObservedAt: time.Now(), Resources: []controlplane.ResourceHealth{{Resource: "node", Status: controlplane.ResourceHealthHealthy}}}
	post := func() int {
		raw, err := json.Marshal(projection)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/agents/agent/projections/resources", bytes.NewReader(raw)))
		return w.Code
	}
	require.Equal(t, 202, post())
	projection.Sequence = 1
	projection.ObservedAt = projection.ObservedAt.Add(time.Hour)
	projection.Resources[0].Status = controlplane.ResourceHealthDrifted
	require.Equal(t, 409, post())
	stored, err := s.apiStore.LoadResourceProjection(context.Background(), "lab")
	require.NoError(t, err)
	require.Equal(t, uint64(2), stored.Sequence)
	cached := s.agents.ListResourceProjections("lab")
	require.Len(t, cached, 1)
	require.Equal(t, controlplane.ResourceHealthHealthy, cached[0].Resources[0].Status)
}
