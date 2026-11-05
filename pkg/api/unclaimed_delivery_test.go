package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestAssignedDestroyAcknowledgedBeforeClaimRecoversDelivery(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		for _, scenario := range []string{"unclaimed", "started", "claimed", "grant", "stopped grant", "live delivery", "maintenance"} {
			t.Run(scenario, func(t *testing.T) {
				ctx := context.Background()
				id := "run-" + scenario
				id = replaceSpaces(id)
				now := time.Now().UTC()
				run := controlplane.Run{ID: id, Topology: id, AgentID: "agent", Op: "destroy", Operation: "destroy", Revision: "revision", QueuedAt: now.Add(-time.Minute), Status: controlplane.RunAssigned}
				require.NoError(t, store.SaveRun(ctx, run))
				command := controlplane.AgentCommand{ID: "cmd-" + id, AgentID: "agent", Type: "run_assigned", Run: &run, ActionID: run.ID, TopologyUID: id, Status: controlplane.AgentCommandStatusAcknowledged, LeaseOwner: "old-worker", LeaseUntil: now.Add(-time.Second), Attempt: 1}
				if scenario == "started" {
					command.Status = controlplane.AgentCommandStatusRunning
				}
				if scenario == "live delivery" {
					command.LeaseUntil = now.Add(time.Hour)
				}
				require.NoError(t, store.SaveAgentCommand(ctx, command))
				if scenario == "claimed" {
					_, ok, err := store.ClaimRun(ctx, run.ID, run.AgentID, "running-worker", time.Minute)
					require.NoError(t, err)
					require.True(t, ok)
				}
				if scenario == "grant" || scenario == "stopped grant" {
					authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
					session, err := authority.OpenSession(ctx, id, id, "")
					require.NoError(t, err)
					grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: id, SessionID: session.ID, CommandID: command.ID, ActionID: run.ID, Attempt: 1, Operation: "run_destroy", Digest: "digest", TargetBootID: "boot", Scope: controlplane.ExecutionScope{TopologyUID: id}}, time.Minute)
					require.NoError(t, err)
					if scenario == "stopped grant" {
						require.NoError(t, authority.AcceptHostStop(ctx, *grant, "stop-proof"))
					}
				}
				if scenario == "maintenance" {
					authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
					_, err := authority.BeginMaintenance(ctx, "freeze-"+id, id, "snapshot-digest")
					require.NoError(t, err)
				}
				service := newAgentService(NewServer(t.TempDir(), t.TempDir()))
				service.store = store
				published := 0
				service.wakeup = func(string, controlplane.AgentCommand) error { published++; return nil }
				require.NoError(t, service.RepublishPendingCommands(ctx))
				got, err := service.FindCommand(ctx, "agent", command.ID)
				require.NoError(t, err)
				if scenario == "unclaimed" || scenario == "started" {
					require.Equal(t, controlplane.AgentCommandStatusQueued, got.Status)
					require.Empty(t, got.LeaseOwner)
					require.Equal(t, 1, got.Attempt)
					leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", command.ID, "new-worker", time.Minute)
					require.NoError(t, err)
					require.True(t, ok)
					require.Equal(t, 2, leased.Attempt)
					stale := controlplane.AgentCommandEvent{AgentID: "agent", CommandID: command.ID, Type: command.Type, Status: "started", ActionID: command.ActionID, Attempt: 1, LeaseOwner: "old-worker"}
					_, err = store.ApplyAgentCommandEvent(ctx, stale)
					require.Error(t, err)
					require.Positive(t, published)
				} else {
					require.Equal(t, command.Status, got.Status)
				}
			})
		}
	})
}
func replaceSpaces(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == ' ' {
			b[i] = '-'
		}
	}
	return string(b)
}
