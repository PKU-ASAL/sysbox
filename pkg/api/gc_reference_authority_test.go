package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestGCGrantRequiresCurrentUnreferencedLifecycle(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
		st, err := manager.Load()
		require.NoError(t, err)
		inventory, err := repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		request := func(id, digest string) error {
			cmd := controlplane.AgentCommand{ID: id, AgentID: "agent", Type: "gc_delete", Status: controlplane.AgentCommandStatusLeased, TopologyUID: st.TopologyUID, ActionID: id, ResourceUID: "orphan-uid", Generation: 1, Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute), Session: &controlplane.ConsoleSession{ID: session.ID, Topology: "lab", AgentID: "agent"}, GCKind: "container", GCExternalID: "orphan", GCInventoryDigest: digest}
			require.NoError(t, store.SaveAgentCommand(ctx, cmd))
			grant, err := s.issueCommandGrant(ctx, "agent", id, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: "worker", Attempt: 1, BootID: "boot"})
			if err == nil {
				require.NoError(t, authority.AcceptHostStop(ctx, *grant, "stop-"+id))
			} else {
				require.Nil(t, grant)
			}
			return err
		}
		require.ErrorIs(t, request("invented", "nonempty-but-invented"), persistence.ErrConflict)
		require.NoError(t, request("valid", inventory.Digest))
		st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "vm"), Driver: "docker", ExternalID: "orphan", OwnerUID: st.TopologyUID, ResourceUID: "orphan-uid", Generation: 1}}
		require.NoError(t, manager.Save(st))
		require.ErrorIs(t, request("stale", inventory.Digest), persistence.ErrConflict)
		inventory, err = repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		require.ErrorIs(t, request("referenced", inventory.Digest), persistence.ErrConflict)
		_, err = manager.Snapshot(ctx, "retain")
		require.NoError(t, err)
		st.Resources = nil
		require.NoError(t, manager.Save(st))
		inventory, err = repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		require.ErrorIs(t, request("snapshot", inventory.Digest), persistence.ErrConflict)
	})
}

func TestGCGrantCannotAuthorizeOrRenewAfterReferenceChange(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		inventory, err := repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		cmd := controlplane.AgentCommand{ID: "gc", AgentID: "agent", Type: "gc_delete", Status: controlplane.AgentCommandStatusLeased, TopologyUID: inventory.TopologyUID, ActionID: "gc", ResourceUID: "orphan-uid", Generation: 1, Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().Add(time.Minute), Session: &controlplane.ConsoleSession{ID: session.ID, Topology: "lab", AgentID: "agent"}, GCKind: "container", GCExternalID: "orphan", GCInventoryDigest: inventory.Digest}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: "worker", Attempt: 1, BootID: "boot"}
		grant, err := s.issueCommandGrant(ctx, "agent", cmd.ID, proof)
		require.NoError(t, err)
		checked := persistence.NewExecutionGrantRepository(s.commandGrantStorage(cmd.ID, func(record *persistence.CommandRecord) error {
			_, err := s.sourceGrant(ctx, "agent", proof, record)
			return err
		}))
		require.NoError(t, checked.Authorize(ctx, *grant))
		manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
		st, err := manager.Load()
		require.NoError(t, err)
		st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "vm"), Driver: "docker", ExternalID: "orphan", OwnerUID: st.TopologyUID, ResourceUID: "orphan-uid", Generation: 1}}
		require.NoError(t, manager.Save(st))
		require.ErrorIs(t, checked.Authorize(ctx, *grant), persistence.ErrConflict)
		_, err = checked.Renew(ctx, *grant, time.Minute)
		require.ErrorIs(t, err, persistence.ErrConflict)
		retained, err := authority.InspectGrant(ctx, "agent", grant.Scope)
		require.NoError(t, err)
		require.Equal(t, grant.ExpiresAt, retained.ExpiresAt, "rejected renewal must not extend authority")
		require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"), "stops remain valid after reference changes")
	})
}
