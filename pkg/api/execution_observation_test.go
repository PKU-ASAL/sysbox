package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
)

func observationTestSession(t *testing.T, store apiStore, agent string) string {
	t.Helper()
	s, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).OpenSession(context.Background(), agent, "test-process", "")
	require.NoError(t, err)
	return s.ID
}
func bindObservationTestBody(t *testing.T, s *Server, id string, body *bytes.Buffer) *bytes.Buffer {
	t.Helper()
	var value map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body.Bytes(), &value))
	var inv controlplane.AgentInventory
	var projections []controlplane.ResourceProjection
	if _, ok := value["topology"]; ok {
		var p controlplane.ResourceProjection
		require.NoError(t, json.Unmarshal(body.Bytes(), &p))
		if p.AgentID == "" {
			p.AgentID = id
		}
		if p.Workspace == "" {
			p.Workspace = p.Topology
		}
		if p.ObservedAt.IsZero() {
			p.ObservedAt = time.Now().UTC()
		}
		if len(p.Resources) == 0 {
			p.Resources = p.Health.Resources
		}
		if p.SessionID == "" {
			p.SessionID = observationTestSession(t, s.apiStore, id)
		}
		if p.Sequence == 0 {
			p.Sequence = 1
		}
		projections = []controlplane.ResourceProjection{p}
		inv = controlplane.AgentInventory{AgentID: id, SessionID: p.SessionID, Sequence: p.Sequence, ObservedAt: time.Now().UTC()}
	} else {
		require.NoError(t, json.Unmarshal(body.Bytes(), &inv))
		inv.AgentID = id
		if inv.SessionID == "" {
			inv.SessionID = observationTestSession(t, s.apiStore, id)
		}
		if inv.Sequence == 0 {
			inv.Sequence = 1
		}
		if inv.ObservedAt.IsZero() {
			inv.ObservedAt = time.Now().UTC()
		}
	}
	seed := sha256.Sum256([]byte("test-host/" + id))
	key := ed25519.NewKeyFromSeed(seed[:])
	if s.grantHostKeys == nil {
		s.grantHostKeys = map[string]ed25519.PublicKey{}
	}
	s.grantHostKeys[id] = key.Public().(ed25519.PublicKey)
	signed, err := controlplane.SignHostObservation(key, controlplane.HostObservation{AgentID: id, SessionID: inv.SessionID, Sequence: inv.Sequence, BootID: "test-boot", ObservedAt: time.Now().UTC(), Inventory: inv, Projections: projections})
	require.NoError(t, err)
	raw, err := json.Marshal(signed)
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
	projection := controlplane.ResourceProjection{AgentID: "agent", SessionID: observationTestSession(t, s.apiStore, "agent"), Sequence: 2, Topology: "lab", ObservedAt: time.Now(), Resources: []model.ResourceHealth{{Resource: "node", Status: model.ResourceHealthHealthy}}}
	post := func() int {
		raw, err := json.Marshal(projection)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/agents/agent/observations", bindObservationTestBody(t, s, "agent", bytes.NewBuffer(raw))))
		return w.Code
	}
	require.Equal(t, 202, post())
	projection.Sequence = 1
	projection.ObservedAt = projection.ObservedAt.Add(time.Second)
	projection.Resources[0].Status = model.ResourceHealthDrifted
	require.Equal(t, 409, post())
	stored, err := s.apiStore.LoadResourceProjection(context.Background(), "lab")
	require.NoError(t, err)
	require.Equal(t, uint64(2), stored.Sequence)
	cached := s.agents.ListResourceProjections("lab")
	require.Len(t, cached, 1)
	require.Equal(t, model.ResourceHealthHealthy, cached[0].Resources[0].Status)
}
