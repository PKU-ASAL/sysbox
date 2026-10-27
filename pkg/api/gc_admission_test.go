package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGCAdmissionReservesReferencesAndRejectsActiveAuthority(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		inventory, err := repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		reservation := persistence.GCReservation{ID: "gc", AgentID: "agent", Candidate: controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: inventory.TopologyUID, OwnerUID: inventory.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: inventory.Digest}}
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: inventory.TopologyUID}, AgentID: "agent", SessionID: session.ID, ActionID: "busy", CommandID: "busy", Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "boot"}, time.Minute)
		require.NoError(t, err)
		require.ErrorIs(t, s.reserveGCWithAuthority(ctx, "lab", reservation), persistence.ErrConflict)
		require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"))
		require.NoError(t, s.reserveGCWithAuthority(ctx, "lab", reservation))
		require.NoError(t, s.reserveGCWithAuthority(ctx, "lab", reservation))
		m, err := s.stateManager("lab")
		require.NoError(t, err)
		st, err := m.Load()
		require.NoError(t, err)
		require.ErrorIs(t, m.Save(st), persistence.ErrConflict)
		require.ErrorIs(t, store.SaveRun(ctx, controlplane.Run{ID: "new-run", Topology: "lab", Status: controlplane.RunQueued, Op: "apply", Operation: "apply", QueuedAt: time.Now()}), persistence.ErrConflict)
	})
}
