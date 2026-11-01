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

func claimedReceiptRun(t *testing.T, s controlstore.Store, id string) *controlplane.Run {
	t.Helper()
	ctx := context.Background()
	run := controlplane.Run{ID: id, Topology: id, Op: "destroy", Operation: "destroy", AgentID: "owner", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC().Add(-time.Second), Revision: "revision"}
	require.NoError(t, s.SaveRun(ctx, run))
	claimed, ok, err := s.ClaimRun(ctx, id, "owner", "lease-"+id, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	return claimed
}
func receiptFor(run controlplane.Run) *controlplane.CleanupReceipt {
	return &controlplane.CleanupReceipt{Version: 1, Topology: run.Topology, TopologyUID: "topology-uid", AgentID: run.AgentID, RunID: run.ID, QueuedAt: run.QueuedAt, Revision: run.Revision, Attempt: run.Attempt, LeaseOwner: run.LeaseOwner, StateSerial: 3, ObservedAt: time.Now().UTC(), Empty: true}
}
func TestCleanupReceiptAtomicCompletionAndReplay(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		run := claimedReceiptRun(t, store, "receipt")
		f := prepareHostRunFixture(t, stateDeleteTestServer(t, store), *run, "topology-uid")
		result := *run
		result.Cleanup = receiptFor(*run)
		result.MarkFinished(nil, time.Now().UTC())
		done := f.complete(t, result)
		require.Equal(t, result.Cleanup, done.Cleanup)
		require.NotNil(t, done.HostCompletion)
		require.Equal(t, *done, *f.complete(t, result))
		altered := *result.Cleanup
		altered.StateSerial++
		changed := result
		changed.Cleanup = &altered
		require.Equal(t, 409, f.post(t, changed).Code)
		changed.Cleanup = nil
		require.Equal(t, 409, f.post(t, changed).Code)
	})
}
func TestCleanupReceiptRejectsForgedBindingAndAllowsExpiredLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		run := claimedReceiptRun(t, store, "binding")
		f := prepareHostRunFixture(t, stateDeleteTestServer(t, store), *run, "topology-uid")
		for _, mutate := range []func(*controlplane.CleanupReceipt){
			func(r *controlplane.CleanupReceipt) { r.Version = 2 }, func(r *controlplane.CleanupReceipt) { r.Empty = false },
			func(r *controlplane.CleanupReceipt) { r.AgentID = "other" }, func(r *controlplane.CleanupReceipt) { r.Topology = "other" },
			func(r *controlplane.CleanupReceipt) { r.RunID = "other" }, func(r *controlplane.CleanupReceipt) { r.TopologyUID = "" },
			func(r *controlplane.CleanupReceipt) { r.TopologyUID = "different-topology" },
			func(r *controlplane.CleanupReceipt) { r.QueuedAt = r.QueuedAt.Add(-time.Hour) }, func(r *controlplane.CleanupReceipt) { r.Revision = "other" },
			func(r *controlplane.CleanupReceipt) { r.Attempt++ }, func(r *controlplane.CleanupReceipt) { r.LeaseOwner = "other" },
			func(r *controlplane.CleanupReceipt) { r.StateSerial = -1 }, func(r *controlplane.CleanupReceipt) { r.ObservedAt = run.StartedAt.Add(-time.Second) },
			func(r *controlplane.CleanupReceipt) { r.StateSerial = 0 }, func(r *controlplane.CleanupReceipt) { r.ObservedAt = time.Now().Add(time.Hour) },
		} {
			result := *run
			result.Cleanup = receiptFor(*run)
			mutate(result.Cleanup)
			result.MarkFinished(nil, time.Now().UTC())
			require.Equal(t, 409, f.post(t, result).Code)
		}
		_, expired, err := store.ExpireRunLease(ctx, run.ID, run.LeaseUntil.Add(time.Second))
		require.NoError(t, err)
		require.True(t, expired)
		result := *run
		result.Cleanup = receiptFor(*run)
		result.MarkFinished(nil, time.Now().UTC())
		done := f.complete(t, result)
		require.Equal(t, controlplane.RunDone, done.Status)
		require.NotNil(t, done.Cleanup, "historical host evidence survives worker lease expiry")
	})
}
func TestCleanupReceiptCannotBeForgedThroughSaveOrLateCompletion(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		run := claimedReceiptRun(t, store, "ordinary")
		forged := *run
		forged.Cleanup = receiptFor(*run)
		require.Error(t, store.SaveRun(ctx, forged))
		forged.ID = "new"
		forged.Topology = "new"
		require.Error(t, store.SaveRun(ctx, forged))
		_, _, err := store.CreateRunDispatch(ctx, incarnationDispatch(forged))
		require.Error(t, err)
		f := prepareHostRunFixture(t, stateDeleteTestServer(t, store), *run, "topology-uid")
		result := *run
		result.MarkFinished(nil, time.Now().UTC())
		require.Nil(t, f.complete(t, result).Cleanup)
		result.Cleanup = receiptFor(*run)
		require.Equal(t, 409, f.post(t, result).Code, "sealed result cannot acquire a new cleanup proof")
	})
}
func TestCleanupReceiptRequiresSuccessfulWholeDestroyAndRejectsRestartInjection(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		run := claimedReceiptRun(t, store, "result")
		f := prepareHostRunFixture(t, stateDeleteTestServer(t, store), *run, "topology-uid")
		for _, status := range []controlplane.RunStatus{controlplane.RunFailed, controlplane.RunCancelled} {
			result := *run
			result.Cleanup = receiptFor(*run)
			result.Status = status
			result.EndedAt = time.Now().UTC()
			require.Equal(t, 409, f.post(t, result).Code)
		}
		apply := controlplane.Run{ID: "apply", Topology: "apply", Op: "apply", Status: controlplane.RunDone, OperationKey: "key", QueuedAt: time.Now().Add(-time.Hour)}
		require.NoError(t, store.SaveRun(ctx, apply))
		next := apply
		next.Status = controlplane.RunQueued
		next.QueuedAt = time.Now()
		next.Cleanup = receiptFor(*run)
		_, err := store.RestartRun(ctx, next)
		require.ErrorIs(t, err, persistence.ErrConflict)
	})
}
