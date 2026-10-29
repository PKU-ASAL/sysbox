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
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestHostObservationRejectsForgeryAndPublishesAtomically(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "agent"}))
		repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := repo.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		o := controlplane.HostObservation{AgentID: "agent", SessionID: session.ID, Sequence: 1, BootID: "host", ObservedAt: time.Now().UTC(), Inventory: controlplane.AgentInventory{AgentID: "agent", SessionID: session.ID, Sequence: 1, ObservedAt: time.Now().UTC()}}
		post := func(o controlplane.HostObservation) int {
			raw, err := json.Marshal(o)
			require.NoError(t, err)
			r := httptest.NewRequest("POST", "/v1/agents/agent/observations", bytes.NewReader(raw))
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, r)
			return w.Code
		}
		require.Equal(t, 401, post(o))
		signed, err := controlplane.SignHostObservation(key, o)
		require.NoError(t, err)
		require.Equal(t, 202, post(signed))
		changed := signed
		changed.Inventory.Resources.CPU = 999
		require.Equal(t, 401, post(changed))
		projection := controlplane.ResourceProjection{AgentID: "agent", SessionID: session.ID, Sequence: 3, Topology: "lab", ObservedAt: time.Now().UTC()}
		require.NoError(t, store.SaveResourceProjection(ctx, projection))
		o.Sequence, o.Inventory.Sequence = 2, 2
		projection.Sequence = 2
		o.Projections = []controlplane.ResourceProjection{projection}
		signed, err = controlplane.SignHostObservation(key, o)
		require.NoError(t, err)
		require.Equal(t, 409, post(signed))
		inv, err := store.GetAgentInventory(ctx, "agent")
		require.NoError(t, err)
		require.Equal(t, uint64(1), inv.Sequence, "projection conflict must roll back inventory too")
		_, err = repo.OpenSession(ctx, "agent", "replacement", session.ID)
		require.NoError(t, err)
		o.Sequence, o.Inventory.Sequence = 4, 4
		o.Projections = nil
		signed, err = controlplane.SignHostObservation(key, o)
		require.NoError(t, err)
		require.Equal(t, 409, post(signed))
	})
}

func TestHostObservationRejectsExpiredSnapshot(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
	require.NoError(t, s.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: "agent"}))
	repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	session, err := repo.OpenSession(context.Background(), "agent", "worker", "")
	require.NoError(t, err)
	observed := time.Now().Add(-10 * time.Minute)
	o, err := controlplane.SignHostObservation(key, controlplane.HostObservation{AgentID: "agent", SessionID: session.ID, Sequence: 1, BootID: "boot", ObservedAt: observed, Inventory: controlplane.AgentInventory{AgentID: "agent", SessionID: session.ID, Sequence: 1, ObservedAt: observed}})
	require.NoError(t, err)
	raw, err := json.Marshal(o)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/agents/agent/observations", bytes.NewReader(raw)))
	require.Equal(t, 401, w.Code, w.Body.String())
}
