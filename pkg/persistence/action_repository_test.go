package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestConfiguredRepositoryRejectsStandaloneState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":6,"resources":[]}`), 0600))
	_, err := OpenConfigured(context.Background(), "", path)
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestDurableSQLiteRejectsEphemeralLocation(t *testing.T) {
	for _, path := range []string{"", ":memory:"} {
		_, err := OpenSQLite(path)
		require.Error(t, err)
	}
}

func TestConfiguredTopologyCannotSilentlySelectAnotherAggregate(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenConfigured(ctx, "sqlite://"+filepath.Join(t.TempDir(), "state.db")+"?topology=selected", "")
	require.NoError(t, err)
	defer repo.Close()
	_, err = repo.LoadState(ctx, "other")
	require.Error(t, err)
	_, err = repo.LoadState(ctx, "selected")
	require.NoError(t, err)
}
func TestConfiguredSQLiteAuthorityUsesDurableFile(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	first, err := OpenConfigured(ctx, "sqlite://review.db", "")
	require.NoError(t, err)
	st, err := first.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, first.Close())
	second, err := OpenConfigured(ctx, "sqlite://review.db", "")
	require.NoError(t, err)
	defer second.Close()
	again, err := second.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, st.TopologyUID, again.TopologyUID)
}

func TestRepositoryDeleteInvalidatesOldTopologyIdentity(t *testing.T) {
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer r.Close()
	ctx := context.Background()
	m := state.NewManagerWithBackend(r.StateBackend("lab"))
	old, err := m.Load()
	require.NoError(t, err)
	require.NoError(t, m.Save(old))
	require.NoError(t, m.Delete(ctx))
	next, err := m.Load()
	require.NoError(t, err)
	require.NotEqual(t, old.TopologyUID, next.TopologyUID)
	require.Error(t, m.Save(old))
	listed, err := r.ListTopologies(ctx)
	require.NoError(t, err)
	require.Empty(t, listed)
}
func TestSQLRepositoryRejectsLegacyTopologyInsteadOfCreatingEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE sysbox_state(topology TEXT PRIMARY KEY,data BLOB); INSERT INTO sysbox_state VALUES ('lab','{}')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	repo, err := OpenSQLite(path)
	require.NoError(t, err)
	defer repo.Close()
	_, err = repo.LoadState(context.Background(), "lab")
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestActionRepositoryContract(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "repository")
			open := func() *Repository {
				var r *Repository
				var err error
				if kind == "sqlite" {
					r, err = OpenSQLite(path)
				} else {
					r, err = OpenFilesystem(path)
				}
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, r.Close()) })
				return r
			}
			r := open()
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.NotEmpty(t, st.TopologyUID)
			addr := address.Resource("sysbox_node", "web")
			action := runtime.ActionRecord{ID: "a", Topology: "lab", RunID: "run", Resource: addr, Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: st.TopologyUID + "/web", Generation: 1, StateSerial: 0}
			require.NoError(t, r.CommitActionIntent(ctx, action))
			action.Revision++
			conflict := action
			conflict.ID = "b"
			conflict.Revision = 0
			require.ErrorIs(t, r.CommitActionIntent(ctx, conflict), ErrConflict)
			candidate := state.Resource{Address: addr, Driver: "docker", ExternalID: "container", OwnerUID: action.OwnerUID, ResourceUID: action.ResourceUID, Generation: 1}
			action.Candidate = &candidate
			action.Status = runtime.ActionSucceeded
			action.EffectComplete = true
			patch := &runtime.StatePatch{Resource: addr.String(), Action: action.Action, Op: runtime.StatePatchUpsert, State: &runtime.StateResourceLog{Type: addr.Type, Name: addr.Name, Provider: "docker", ExternalID: "container"}}
			invalid := action
			badCandidate := candidate
			badCandidate.OwnerUID = "foreign"
			invalid.Candidate = &badCandidate
			require.ErrorIs(t, r.CommitActionOutcome(ctx, invalid, patch), ErrCorruptRecord)
			unchanged, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.Empty(t, unchanged.Resources, "rejected outcome must not partially commit state")
			require.Zero(t, unchanged.Meta.Serial)
			journal, err := r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, runtime.ActionIntentCommitted, journal[0].Status)
			require.Equal(t, int64(1), journal[0].Revision, "rejected outcome must not advance journal")
			require.NoError(t, r.CommitActionOutcome(ctx, action, patch))
			st, err = r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, st.Resources, 1)
			require.Equal(t, int64(1), st.Meta.Serial)
			require.Equal(t, action.ResourceUID, st.Resources[0].ResourceUID)
			require.NoError(t, r.CommitActionOutcome(ctx, action, patch), "identical completion is idempotent")
			reopened := open()
			history, err := reopened.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, history, 1)
			require.Equal(t, runtime.ActionSucceeded, history[0].Status)
			manager := state.NewManagerWithBackend(reopened.StateBackend("lab"))
			loaded, err := manager.Load()
			require.NoError(t, err)
			require.Equal(t, int64(1), loaded.Meta.Serial)
			require.NoError(t, manager.Save(loaded))
			require.Error(t, r.CommitActionIntent(ctx, conflict), "old serial cannot start another action")
		})
	}
}

func TestActionOutcomeRejectsStaleJournalRevision(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var repo *Repository
			var err error
			if kind == "sqlite" {
				repo, err = OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
			} else {
				repo, err = OpenFilesystem(t.TempDir())
			}
			require.NoError(t, err)
			defer repo.Close()
			st, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			a := runtime.ActionRecord{ID: "a", Topology: "lab", RunID: "run", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "resource-web", Generation: 1}
			require.NoError(t, repo.CommitActionIntent(ctx, a))
			listed, err := repo.ListTopologies(ctx)
			require.NoError(t, err)
			require.Len(t, listed, 1, "pending create must be discoverable before the first resource commit")
			require.True(t, listed[0].HasState)
			a.Revision++
			a.Status = runtime.ActionUncertain
			stale := a
			a.EffectComplete = true
			a.Phase = "verifying"
			a.Candidate = &state.Resource{Address: a.Resource, Driver: "docker", ExternalID: "container", OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: 1}
			require.NoError(t, repo.CommitActionOutcome(ctx, a, nil))
			require.ErrorIs(t, repo.CommitActionOutcome(ctx, stale, nil), ErrConflict)
			loaded, err := repo.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, loaded, 1)
			require.Equal(t, int64(2), loaded[0].Revision)
			require.True(t, loaded[0].EffectComplete)
			require.Equal(t, "container", loaded[0].Candidate.ExternalID)
			stale.Revision = loaded[0].Revision
			require.ErrorIs(t, repo.CommitActionOutcome(ctx, stale, nil), ErrConflict, "even a current revision must not discard recovery evidence")
		})
	}
}
func TestResetAcceptedPhaseCannotRegressAfterPrepare(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r := resetTestRepository(t, kind)
			manager := state.NewManagerWithBackend(r.StateBackend("lab"))
			st, err := manager.Load()
			require.NoError(t, err)
			addr := address.Resource("sysbox_node", "web")
			st.Resources = []state.Resource{{Address: addr, OwnerUID: st.TopologyUID, ResourceUID: "web", Generation: 1, Driver: "docker", ExternalID: "original"}}
			require.NoError(t, manager.Save(st))
			a := runtime.ActionRecord{ID: "reset", Topology: "lab", RunID: "run", Resource: addr, Action: model.PlanActionReset, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "web", Generation: 2, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Phase: "accepted", Candidate: &st.Resources[0]}
			for _, phase := range []string{"", "preparing", "prepared", "destroying", "destroy_returned", "destroyed", "applying", "verifying"} {
				invalid := a
				invalid.Phase = phase
				require.Error(t, r.CommitActionIntent(ctx, invalid), "invalid initial phase %q", phase)
			}
			require.NoError(t, r.CommitActionIntent(ctx, a))
			a.Revision = 1
			for _, phase := range []string{"prepared", "destroying", "destroy_returned", "destroyed", "applying", "verifying"} {
				invalid := a
				invalid.Phase, invalid.Status = phase, runtime.ActionUncertain
				require.Error(t, r.CommitActionOutcome(ctx, invalid, nil), "cannot skip preparing")
			}
			a.Phase, a.Status = "preparing", runtime.ActionUncertain
			require.NoError(t, r.CommitActionOutcome(ctx, a, nil))
			a.Revision++
			a.Phase = "accepted"
			require.ErrorIs(t, r.CommitActionOutcome(ctx, a, nil), ErrConflict)
			stored, err := r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, "preparing", stored[0].Phase)
			a = stored[0]
			for _, phase := range []string{"prepared", "destroying", "destroy_returned", "destroyed", "applying", "verifying"} {
				a.Phase = phase
				a.EffectComplete = phase == "verifying"
				if a.EffectComplete {
					replacement := *a.Candidate
					replacement.Generation = a.Generation
					replacement.ExternalID = "replacement"
					a.Candidate = &replacement
				}
				invalid := a
				invalid.EffectComplete = !a.EffectComplete
				require.Error(t, r.CommitActionOutcome(ctx, invalid, nil), "phase and evidence must agree")
				if phase == "destroying" {
					start := make(chan struct{})
					results := make(chan error, 2)
					for range 2 {
						go func(action runtime.ActionRecord) {
							<-start
							results <- r.CommitActionOutcome(ctx, action, nil)
						}(a)
					}
					close(start)
					wins := 0
					for range 2 {
						if err := <-results; err == nil {
							wins++
						} else {
							require.ErrorIs(t, err, ErrConflict)
						}
					}
					require.Equal(t, 1, wins)
					require.ErrorIs(t, r.CommitActionOutcome(ctx, a, nil), ErrConflict, "second executor with the same prepared revision cannot claim destroy")
				} else {
					require.NoError(t, r.CommitActionOutcome(ctx, a, nil), phase)
				}
				a.Revision++
			}
		})
	}
}

func TestResetBatchAdmissionRollsBackEveryIntent(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r := resetTestRepository(t, kind)
			manager := state.NewManagerWithBackend(r.StateBackend("lab"))
			st, err := manager.Load()
			require.NoError(t, err)
			for _, name := range []string{"first", "second", "third"} {
				st.Resources = append(st.Resources, state.Resource{Address: address.Resource("sysbox_node", name), Driver: "docker", ExternalID: name, OwnerUID: st.TopologyUID, ResourceUID: name, Generation: 1})
			}
			require.NoError(t, manager.Save(st))
			var batch []runtime.ActionRecord
			for _, res := range st.Resources[:2] {
				batch = append(batch, runtime.ActionRecord{ID: res.ResourceUID, RunID: "run", Topology: "lab", Resource: res.Address, Action: model.PlanActionReset, Status: runtime.ActionIntentCommitted, Phase: "accepted", OwnerUID: st.TopologyUID, ResourceUID: res.ResourceUID, Generation: 2, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Candidate: &res})
			}
			bad := append([]runtime.ActionRecord(nil), batch...)
			bad[1].ResourceUID = "foreign"
			require.Error(t, r.CommitResetIntents(ctx, bad))
			got, err := r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Empty(t, got)
			require.NoError(t, r.CommitResetIntents(ctx, batch))
			require.NoError(t, r.CommitResetIntents(ctx, batch), "exact batch retry")
			require.NoError(t, r.CommitResetIntents(ctx, []runtime.ActionRecord{batch[1], batch[0]}), "retry order does not change membership")
			require.ErrorIs(t, r.CommitResetIntents(ctx, batch[:1]), ErrConflict, "retry cannot omit an admitted action")
			replaced := append([]runtime.ActionRecord(nil), batch...)
			replaced[1].ID = "different-action"
			require.ErrorIs(t, r.CommitResetIntents(ctx, replaced), ErrConflict, "retry cannot replace an admitted action ID")
			third := batch[0]
			third.ID, third.ResourceUID = "third", "third"
			third.Resource, third.Candidate = st.Resources[2].Address, &st.Resources[2]
			expanded := append(append([]runtime.ActionRecord(nil), batch...), third)
			require.ErrorIs(t, r.CommitResetIntents(ctx, expanded), ErrConflict, "retry cannot append another valid target")
			got, err = r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, got, 2)
			before := append([]runtime.ActionRecord(nil), got...)
			for _, field := range []string{"incarnation", "digest", "resource_uid"} {
				changed := append([]runtime.ActionRecord(nil), batch...)
				switch field {
				case "incarnation":
					for i := range changed {
						changed[i].RunRevision = "changed"
					}
				case "digest":
					changed[0].DesiredDigest = "changed"
				case "resource_uid":
					changed[0].ResourceUID = "foreign"
				}
				require.ErrorIs(t, r.CommitResetIntents(ctx, changed), ErrConflict, field)
				got, err = r.LoadActions(ctx, "lab")
				require.NoError(t, err)
				require.Equal(t, before, got, "rejected retry must preserve records")
			}
			advanced := got[0]
			advanced.Phase, advanced.Status = "preparing", runtime.ActionUncertain
			require.NoError(t, r.CommitActionOutcome(ctx, advanced, nil))
			before, err = r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.NoError(t, r.CommitResetIntents(ctx, batch), "original batch replay after progress")
			got, err = r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, before, got, "replay must preserve phase and revision")
		})
	}
}

func TestConcurrentRepositoryIntentsHaveOneWinner(t *testing.T) {
	root := t.TempDir()
	a, _ := OpenFilesystem(root)
	b, _ := OpenFilesystem(root)
	defer a.Close()
	defer b.Close()
	st, err := a.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, r := range []*Repository{a, b} {
		wg.Go(func() {
			results <- r.CommitActionIntent(context.Background(), runtime.ActionRecord{ID: string(rune('a' + i)), Topology: "lab", RunID: "run", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: st.TopologyUID + "/web", Generation: 1})
		})
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else {
			require.ErrorIs(t, err, ErrConflict)
		}
	}
	require.Equal(t, 1, won)
}

func TestLegacyAggregateVersionsAreRejectedWithoutImport(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4, 5} {
		raw := []byte(fmt.Sprintf(`{"format":%d}`, version))
		_, err := decodeAggregate(raw)
		require.ErrorIs(t, err, ErrUnsupportedVersion)
	}
}
