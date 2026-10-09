package api

import (
	"context"
	"errors"
	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5/pgconn"
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

func TestRecoveryOutcomeRevalidatesLeaseInsideCommit(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		scenarios := []string{"live", "revoked", "wrong_owner", "wrong_attempt", "blocked_agent", "active_successor", "foreign_run", "expired", "maintenance"}
		switch store.(type) {
		case *localAPIStore, *postgresAPIStore:
			scenarios = append(scenarios, "serialized_revoke", "expired_after_wait", "serialized_freeze")
		}
		for _, scenario := range scenarios {
			t.Run(scenario, func(t *testing.T) {
				ctx := context.Background()
				s := NewServer(t.TempDir(), t.TempDir())
				s.apiStore = store
				switch typed := store.(type) {
				case *localAPIStore:
					s.runsDir = typed.runsDir
				case *sqliteAPIStore:
					s.stateBackend = "sqlite://" + typed.dbPath
				case *postgresAPIStore:
					s.stateBackend = typed.dsn
				}
				topology := scenario
				agentID := "agent-" + topology
				run := controlplane.Run{ID: topology, Topology: topology, AgentID: agentID, Op: "apply", Status: controlplane.RunFailed, Recoverable: true, QueuedAt: time.Now().UTC(), Revision: "revision", Attempt: 1, LeaseOwner: "old", LeaseUntil: time.Now().Add(-time.Minute)}
				require.NoError(t, store.SaveRun(ctx, run))
				require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: agentID, Capabilities: []string{controlplane.RunRecoveryCapability}}))
				command := controlplane.AgentCommand{ID: "recover-" + topology, AgentID: agentID, Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryRecover, Run: &run}
				require.NoError(t, store.SaveAgentCommand(ctx, command))
				ttl := time.Minute
				if scenario == "expired_after_wait" {
					ttl = time.Second
				}
				if scenario == "expired" {
					ttl = time.Nanosecond
				}
				lease, ok, err := store.AcquireAgentCommandLease(ctx, agentID, command.ID, "worker", ttl)
				require.NoError(t, err)
				require.True(t, ok)
				repo, err := s.actionRepository(topology)
				require.NoError(t, err)
				st, err := repo.LoadState(ctx, topology)
				require.NoError(t, err)
				a := runtime.ActionRecord{ID: "action", Topology: topology, RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "web"), Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
				require.NoError(t, repo.CommitActionIntent(ctx, a))
				a.Revision, a.Status, a.EffectComplete = 1, runtime.ActionSucceeded, true
				a.Candidate = &state.Resource{Address: a.Resource, OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: a.Generation, ExternalID: "vm"}
				proof := hostsupervisor.RunRecoveryRequest{CommandID: lease.ID, LeaseOwner: lease.LeaseOwner, Attempt: lease.Attempt}
				bound := &recoveryActionRepository{ActionRepository: repo, server: s, proof: proof, run: run}
				switch scenario {
				case "maintenance":
					authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
					_, err := authority.BeginMaintenance(ctx, "restore", st.TopologyUID, "snapshot-digest")
					require.NoError(t, err)
				case "revoked":
					// Retirement happens after observation, before outcome commit.
					require.NoError(t, store.DeleteAgentCommand(ctx, agentID, lease.ID))
				case "wrong_owner":
					bound.proof.LeaseOwner = "different-worker"
				case "wrong_attempt":
					bound.proof.Attempt++
				case "blocked_agent":
					owner, err := store.GetAgent(ctx, agentID)
					require.NoError(t, err)
					owner.Disabled = true
					require.NoError(t, store.SaveAgent(ctx, *owner))
				case "active_successor":
					require.NoError(t, store.SaveRun(ctx, controlplane.Run{ID: topology + "-next", Topology: topology, AgentID: agentID, Status: controlplane.RunAssigned}))
				case "foreign_run":
					bound.run.LeaseOwner = "other-incarnation"
				}
				patch := &runtime.StatePatch{Op: runtime.StatePatchUpsert, Resource: a.Resource.String(), Action: a.Action}
				if scenario == "serialized_revoke" || scenario == "expired_after_wait" || scenario == "serialized_freeze" {
					held, release, holderDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
					defer func() {
						select {
						case <-release:
						default:
							close(release)
						}
					}()
					go func() {
						holderDone <- repo.CommitActionOutcomeWithGuard(ctx, a, patch, func() error { close(held); <-release; return errors.New("test lock released") })
					}()
					<-held
					committed := make(chan error, 1)
					go func() { committed <- bound.CommitActionOutcome(ctx, a, patch) }()
					// Wait for the actual source lock, not a scheduling sleep.
					switch typed := store.(type) {
					case *localAPIStore:
						probe := flock.New(filepath.Join(typed.runsDir, ".run-locks", "records.lock"))
						require.Eventually(t, func() bool {
							locked, err := probe.TryLock()
							if err != nil {
								return false
							}
							if locked {
								_ = probe.Unlock()
							}
							return !locked
						}, time.Second, time.Millisecond)
						require.NoError(t, probe.Close())
					case *postgresAPIStore:
						pool, err := typed.poolFor(ctx)
						require.NoError(t, err)
						require.Eventually(t, func() bool {
							tx, err := pool.Begin(ctx)
							if err != nil {
								return false
							}
							defer tx.Rollback(ctx)
							_, err = tx.Exec(ctx, "SELECT id FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE NOWAIT", lease.ID)
							var pgerr *pgconn.PgError
							return errors.As(err, &pgerr) && pgerr.Code == "55P03"
						}, time.Second, time.Millisecond)
					}
					if scenario == "serialized_freeze" {
						limited, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
						_, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).BeginMaintenance(limited, "blocked-freeze", st.TopologyUID, "snapshot-digest")
						cancel()
						require.Error(t, err, "maintenance cannot pass in-flight recovery commit")
					} else if scenario == "serialized_revoke" {
						limited, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
						err := store.DeleteAgentCommand(limited, agentID, lease.ID)
						cancel()
						require.ErrorIs(t, err, context.DeadlineExceeded, "retirement cannot pass an in-flight aggregate commit")
					} else {
						<-time.After(time.Until(lease.LeaseUntil) + 5*time.Millisecond)
					}
					close(release)
					require.ErrorContains(t, <-holderDone, "test lock released")
					err = <-committed
				} else {
					err = bound.CommitActionOutcome(ctx, a, patch)
				}
				succeeds := scenario == "live" || scenario == "serialized_revoke" || scenario == "serialized_freeze"
				if !succeeds {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				after, err := repo.LoadState(ctx, topology)
				require.NoError(t, err)
				if !succeeds {
					require.Empty(t, after.Resources)
					require.Equal(t, st.Meta.Serial, after.Meta.Serial)
				} else {
					require.Len(t, after.Resources, 1)
				}
			})
		}
	})
}
