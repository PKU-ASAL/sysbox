package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func claimedReceiptRun(t *testing.T, s apiStore, id string) *controlplane.Run {
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
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := claimedReceiptRun(t, s, "receipt")
		receipt := receiptFor(*run)
		report := controlplane.RunCompletion{Run: *run, Cleanup: receipt}
		report.Run.Status = controlplane.RunDone
		done, changed, err := s.CompleteRun(ctx, "owner", run.ID, report)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, receipt, done.Cleanup)
		durable, err := s.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.Equal(t, receipt, durable.Cleanup)
		_, changed, err = s.CompleteRun(ctx, "owner", run.ID, report)
		require.NoError(t, err)
		require.False(t, changed)
		altered := *receipt
		altered.StateSerial++
		report.Cleanup = &altered
		_, _, err = s.CompleteRun(ctx, "owner", run.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict)
		report.Cleanup = nil
		_, _, err = s.CompleteRun(ctx, "owner", run.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict)
	})
}
func TestCleanupReceiptRejectsForgedBindingAndExpiredLease(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := claimedReceiptRun(t, s, "binding")
		for _, mutate := range []func(*controlplane.CleanupReceipt){
			func(r *controlplane.CleanupReceipt) { r.Version = 2 }, func(r *controlplane.CleanupReceipt) { r.Empty = false },
			func(r *controlplane.CleanupReceipt) { r.AgentID = "other" }, func(r *controlplane.CleanupReceipt) { r.Topology = "other" },
			func(r *controlplane.CleanupReceipt) { r.RunID = "other" }, func(r *controlplane.CleanupReceipt) { r.TopologyUID = "" },
			func(r *controlplane.CleanupReceipt) { r.QueuedAt = r.QueuedAt.Add(-time.Hour) }, func(r *controlplane.CleanupReceipt) { r.Revision = "other" },
			func(r *controlplane.CleanupReceipt) { r.Attempt++ }, func(r *controlplane.CleanupReceipt) { r.LeaseOwner = "other" },
			func(r *controlplane.CleanupReceipt) { r.StateSerial = -1 }, func(r *controlplane.CleanupReceipt) { r.ObservedAt = run.StartedAt.Add(-time.Second) },
			func(r *controlplane.CleanupReceipt) { r.StateSerial = 0 },
			func(r *controlplane.CleanupReceipt) { r.ObservedAt = time.Now().Add(time.Hour) },
		} {
			receipt := receiptFor(*run)
			mutate(receipt)
			report := controlplane.RunCompletion{Run: *run, Cleanup: receipt}
			report.Run.Status = controlplane.RunDone
			_, _, err := s.CompleteRun(ctx, "owner", run.ID, report)
			require.ErrorIs(t, err, persistence.ErrConflict)
		}
		expired := *run
		expired.ID = "expired"
		expired.Topology = "expired"
		expired.LeaseUntil = time.Now().Add(-time.Second)
		require.NoError(t, s.SaveRun(ctx, expired))
		report := controlplane.RunCompletion{Run: expired, Cleanup: receiptFor(expired)}
		report.Run.Status = controlplane.RunDone
		_, _, err := s.CompleteRun(ctx, "owner", expired.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict)
	})
}
func TestCleanupReceiptCannotBeForgedThroughSaveOrLateCompletion(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := claimedReceiptRun(t, s, "ordinary")
		forged := *run
		forged.Cleanup = receiptFor(*run)
		require.Error(t, s.SaveRun(ctx, forged))
		forged.ID = "new"
		forged.Topology = "new"
		require.Error(t, s.SaveRun(ctx, forged))
		_, _, err := s.CreateRunDispatch(ctx, incarnationDispatch(forged))
		require.Error(t, err, "outbox admission must not mint cleanup authority")
		report := controlplane.RunCompletion{Run: *run}
		report.Run.Status = controlplane.RunDone
		done, _, err := s.CompleteRun(ctx, "owner", run.ID, report)
		require.NoError(t, err)
		require.Nil(t, done.Cleanup)
		report.Cleanup = receiptFor(*run)
		_, _, err = s.CompleteRun(ctx, "owner", run.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict)
	})
}

func TestCleanupReceiptRequiresSuccessfulWholeDestroyAndRejectsRestartInjection(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := claimedReceiptRun(t, s, "result")
		for _, status := range []controlplane.RunStatus{controlplane.RunFailed, controlplane.RunCancelled} {
			report := controlplane.RunCompletion{Run: *run, Cleanup: receiptFor(*run)}
			report.Run.Status = status
			_, _, err := s.CompleteRun(ctx, "owner", run.ID, report)
			require.ErrorIs(t, err, persistence.ErrConflict)
		}
		report := controlplane.RunCompletion{Run: *run}
		report.Run.Status = controlplane.RunDone
		report.Run.Cleanup = receiptFor(*run)
		_, _, err := s.CompleteRun(ctx, "owner", run.ID, report)
		require.ErrorIs(t, err, persistence.ErrConflict, "nested cleanup cannot bypass explicit report proof")
		apply := controlplane.Run{ID: "apply", Topology: "apply", Op: "apply", Status: controlplane.RunDone, OperationKey: "key", QueuedAt: time.Now().Add(-time.Hour)}
		require.NoError(t, s.SaveRun(ctx, apply))
		next := apply
		next.Status = controlplane.RunQueued
		next.QueuedAt = time.Now()
		next.Cleanup = receiptFor(*run)
		_, err = s.RestartRun(ctx, next)
		require.ErrorIs(t, err, persistence.ErrConflict)
	})
}
