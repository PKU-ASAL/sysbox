package persistence

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func continuationFixture(t *testing.T, r *Repository, names ...string) runtime.ResetContinuationRequest {
	t.Helper()
	ctx := context.Background()
	manager := state.NewManagerWithBackend(r.StateBackend("lab"))
	st, err := manager.Load()
	require.NoError(t, err)
	var plan runtime.Plan
	if len(names) == 0 {
		names = []string{"firecracker", "libvirt", "docker"}
	}
	for _, name := range names {
		addr := address.Resource("sysbox_node", name)
		st.Resources = append(st.Resources, state.Resource{Address: addr, Driver: name, ExternalID: name, OwnerUID: st.TopologyUID, ResourceUID: name, Generation: 1})
		plan.Actions = append(plan.Actions, model.PlannedChange{Address: addr, Action: model.PlanActionReset})
	}
	require.NoError(t, manager.Save(st))
	path := filepath.Join(t.TempDir(), "original.checkpoint.json")
	recorder := runtime.NewFileRecorder(path, "original", "lab")
	recorder.SetStateSerialBefore(st.Meta.Serial)
	require.NoError(t, recorder.Begin("reset", &plan))
	cp, err := runtime.LoadCheckpointFile(path)
	require.NoError(t, err)
	req := runtime.ResetContinuationRequest{ID: "continuation", TopologyUID: st.TopologyUID, RunQueuedAt: time.Now().UTC(), RunRevision: "original-config", ExpectedSerial: st.Meta.Serial, Checkpoint: *cp}
	for i, resource := range st.Resources {
		a := runtime.ActionRecord{ID: resource.ResourceUID, RunID: "original", RunQueuedAt: req.RunQueuedAt, RunRevision: req.RunRevision, Topology: "lab", Resource: resource.Address, Action: model.PlanActionReset, Status: runtime.ActionIntentCommitted, Phase: "accepted", OwnerUID: st.TopologyUID, ResourceUID: resource.ResourceUID, Generation: 2, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Candidate: &resource}
		if i >= 2 {
			req.Missing = append(req.Missing, a)
			continue
		}
		require.NoError(t, r.CommitActionIntent(ctx, a))
		a.Revision = 1
		for _, phase := range []string{"preparing", "prepared", "destroying", "destroy_returned", "destroyed"} {
			a.Phase, a.Status = phase, runtime.ActionUncertain
			require.NoError(t, r.CommitActionOutcome(ctx, a, nil))
			a.Revision++
		}
		req.Existing = append(req.Existing, a)
	}
	return req
}

func TestResetContinuationPreservesOriginalScopeAndProgress(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			req := continuationFixture(t, r)
			ctx := context.Background()
			receipt, err := r.CommitResetContinuation(ctx, req)
			require.NoError(t, err)
			got, err := r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, got, 3)
			for _, original := range req.Existing {
				require.Contains(t, got, original)
			}
			missing := req.Missing[0]
			missing.Revision = 1
			require.Contains(t, got, missing)
			missing.Phase, missing.Status = "preparing", runtime.ActionUncertain
			require.NoError(t, r.CommitActionOutcome(ctx, missing, nil))
			retry, err := r.CommitResetContinuation(ctx, req)
			require.NoError(t, err, "admission replay returns evidence, not permission to replay provider work")
			require.Equal(t, receipt, retry)
			missing.Revision++
			for _, phase := range []string{"prepared", "destroying", "destroy_returned", "destroyed", "applying", "verifying"} {
				missing.Phase = phase
				if phase == "verifying" {
					candidate := *missing.Candidate
					candidate.Generation = missing.Generation
					missing.Candidate = &candidate
					missing.EffectComplete = true
				}
				require.NoError(t, r.CommitActionOutcome(ctx, missing, nil))
				missing.Revision++
			}
			missing.Status = runtime.ActionSucceeded
			require.NoError(t, r.CommitActionOutcome(ctx, missing, &runtime.StatePatch{Resource: missing.Resource.String(), Action: missing.Action, Op: runtime.StatePatchUpsert}))
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.Greater(t, st.Meta.Serial, req.ExpectedSerial)
			retry, err = r.CommitResetContinuation(ctx, req)
			require.NoError(t, err, "exact replay survives state serial advancement")
			require.Equal(t, receipt, retry)
			loaded, err := r.LoadResetContinuation(ctx, "lab", "original")
			require.NoError(t, err)
			require.Equal(t, receipt, loaded)
			changed := req
			changed.ID = "another-continuation"
			_, err = r.CommitResetContinuation(ctx, changed)
			require.ErrorIs(t, err, ErrConflict)
		})
	}
}

func TestResetContinuationRollsBackEarlierMissingIntent(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			req := continuationFixture(t, r, "firecracker", "libvirt", "docker", "fourth")
			req.Missing[1].ResourceUID = "foreign"
			_, err := r.CommitResetContinuation(context.Background(), req)
			require.ErrorIs(t, err, ErrConflict)
			actions, err := r.LoadActions(context.Background(), "lab")
			require.NoError(t, err)
			require.ElementsMatch(t, req.Existing, actions, "neither missing action may be committed")
			_, err = r.LoadResetContinuation(context.Background(), "lab", "original")
			require.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestResetContinuationRejectsStaleOrUnknownAdmission(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		for _, scenario := range []string{"plan-hash", "cross-run", "revision", "serial", "unknown-phase", "missing-target", "replace-id"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				r := resetTestRepository(t, kind)
				req := continuationFixture(t, r)
				ctx := context.Background()
				switch scenario {
				case "plan-hash":
					req.Checkpoint.PlanSHA256 = "tampered"
				case "cross-run":
					req.Missing[0].RunID = "unrelated"
				case "revision":
					req.Existing[0].Revision++
				case "serial":
					req.ExpectedSerial++
				case "unknown-phase":
					a := req.Existing[0]
					a.Phase = "applying"
					require.NoError(t, r.CommitActionOutcome(ctx, a, nil))
					a.Revision++
					req.Existing[0] = a
				case "missing-target":
					req.Missing = nil
				case "replace-id":
					req.Existing[0].ID = "replacement"
				}
				before, err := r.LoadActions(ctx, "lab")
				require.NoError(t, err)
				_, err = r.CommitResetContinuation(ctx, req)
				require.Error(t, err)
				after, err := r.LoadActions(ctx, "lab")
				require.NoError(t, err)
				require.Equal(t, before, after)
				_, err = r.LoadResetContinuation(ctx, "lab", "original")
				require.ErrorIs(t, err, ErrNotFound)
			})
		}
	}
}

func TestResetContinuationConcurrentRequestsHaveOneWinner(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			req := continuationFixture(t, r)
			other := req
			other.ID = "competing"
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, request := range []runtime.ResetContinuationRequest{req, other} {
				wg.Go(func() {
					_, err := r.CommitResetContinuation(context.Background(), request)
					results <- err
				})
			}
			wg.Wait()
			close(results)
			winners := 0
			for err := range results {
				if err == nil {
					winners++
				} else {
					require.ErrorIs(t, err, ErrConflict)
				}
			}
			require.Equal(t, 1, winners)
			actions, err := r.LoadActions(context.Background(), "lab")
			require.NoError(t, err)
			require.Len(t, actions, 3)
		})
	}
}

func TestContinuationFormatRejectsPreviousAggregate(t *testing.T) {
	a := newAggregate()
	a.Format = 4
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	_, err = decodeAggregate(raw)
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestResetContinuationSealedMembershipCannotBeBypassed(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			req := continuationFixture(t, r)
			ctx := context.Background()
			receipt, err := r.CommitResetContinuation(ctx, req)
			require.NoError(t, err)
			extra := req.Missing[0]
			extra.ID, extra.ResourceUID = "extra", "extra"
			extra.Resource = address.Resource("sysbox_node", "extra")
			extra.Action, extra.Phase, extra.Generation, extra.Candidate = model.PlanActionCreate, "", 1, nil
			require.ErrorIs(t, r.CommitActionIntent(ctx, extra), ErrConflict)
			for _, corruption := range []string{"drop-member", "change-digest", "change-receipt"} {
				err := r.update(ctx, "lab", func(a *aggregate) error {
					id := req.Existing[0].ID
					switch corruption {
					case "drop-member":
						delete(a.Actions, id)
					case "change-digest":
						member := a.Actions[id]
						member.DesiredDigest = "tampered"
						a.Actions[id] = member
					case "change-receipt":
						stored := a.ResetContinuations["original"]
						stored.Request.Missing[0].ID = "replacement"
						a.ResetContinuations["original"] = stored
					}
					return nil
				})
				require.ErrorIs(t, err, ErrCorruptRecord, corruption)
				loaded, err := r.LoadResetContinuation(ctx, "lab", "original")
				require.NoError(t, err)
				require.Equal(t, receipt, loaded)
			}
		})
	}
}

func TestResetContinuationRequiresRecordedRunIncarnation(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			req := continuationFixture(t, r)
			req.RunQueuedAt = time.Time{}
			for i := range req.Existing {
				req.Existing[i].RunQueuedAt = time.Time{}
			}
			for i := range req.Missing {
				req.Missing[i].RunQueuedAt = time.Time{}
			}
			require.NoError(t, r.update(context.Background(), "lab", func(a *aggregate) error {
				for id, action := range a.Actions {
					action.RunQueuedAt = time.Time{}
					a.Actions[id] = action
				}
				return nil
			}))
			_, err := r.CommitResetContinuation(context.Background(), req)
			require.ErrorIs(t, err, ErrConflict, "missing identity must not be inferred from checkpoint times")
		})
	}
}
