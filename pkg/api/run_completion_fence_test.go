package api

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRunCompletionRejectsExpiredFailureAndCancellation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		for _, status := range []controlplane.RunStatus{controlplane.RunFailed, controlplane.RunCancelled} {
			ctx := context.Background()
			run := controlplane.Run{ID: string(status), Topology: string(status), Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "old", Attempt: 1, LeaseUntil: time.Now().Add(-time.Minute)}
			require.NoError(t, store.SaveRun(ctx, run))
			registry := newAgentRegistry()
			svc := AgentService{store: store, registry: registry, jobs: newJobsWithRecovery(t.TempDir(), store, false)}
			run.Status = status
			_, err := svc.CompleteRun("agent", run.ID, controlplane.RunCompletion{Run: run})
			require.ErrorIs(t, err, persistence.ErrConflict)
			got, err := store.GetRun(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.RunRunning, got.Status)
			require.Empty(t, registry.ListProjections("agent"))
		}
	})
}

type rejectedRunCompletion struct {
	apiStore
	failure error
}

func (s rejectedRunCompletion) CompleteRun(context.Context, string, string, controlplane.RunCompletion) (*controlplane.Run, bool, error) {
	return nil, false, s.failure
}
func TestRunCompletionFailureDoesNotPublishProjection(t *testing.T) {
	ctx := context.Background()
	store := &localAPIStore{runsDir: t.TempDir()}
	run := controlplane.Run{ID: "failed-commit", Topology: "lab", Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Hour)}
	require.NoError(t, store.SaveRun(ctx, run))
	failure := errors.New("commit failed")
	registry := newAgentRegistry()
	svc := AgentService{store: rejectedRunCompletion{apiStore: store, failure: failure}, registry: registry}
	run.Status = controlplane.RunDone
	_, err := svc.CompleteRun("agent", run.ID, controlplane.RunCompletion{Run: run})
	require.ErrorIs(t, err, failure)
	require.Empty(t, registry.ListProjections("agent"))
	got, err := store.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunRunning, got.Status)
}

func TestRunCompletionReplayDoesNotReplaceNewerProjection(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()
	run := controlplane.Run{ID: "replay", Topology: "lab", Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Hour)}
	require.NoError(t, store.SaveRun(ctx, run))
	registry := newAgentRegistry()
	svc := AgentService{store: store, registry: registry}
	run.Status = controlplane.RunDone
	done, err := svc.CompleteRun("agent", run.ID, controlplane.RunCompletion{Run: run})
	require.NoError(t, err)
	registry.SaveProjection(controlplane.Projection{AgentID: "agent", Topology: "lab", Workspace: "lab", Serial: 99})
	_, err = svc.CompleteRun("agent", run.ID, done)
	require.NoError(t, err)
	projections := registry.ListProjections("agent")
	require.Len(t, projections, 1)
	require.EqualValues(t, 99, projections[0].Serial)
}

func TestRunCompletionPreservesRenewalAndRejectsOldIncarnation(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		ctx := context.Background()
		run := controlplane.Run{ID: "completion", Topology: "lab", Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 2, QueuedAt: time.Now().Add(-time.Hour).UTC(), LeaseUntil: time.Now().Add(time.Minute).UTC()}
		require.NoError(t, s.SaveRun(ctx, run))
		renewed, ok, err := s.RenewRunLease(ctx, run.ID, "agent", "worker", time.Hour)
		require.NoError(t, err)
		require.True(t, ok)
		report := run
		report.Status = controlplane.RunDone
		report.QueuedAt = run.QueuedAt.Add(-time.Hour)
		_, _, err = s.CompleteRun(ctx, "agent", run.ID, controlplane.RunCompletion{Run: report})
		require.ErrorIs(t, err, persistence.ErrConflict)
		report.QueuedAt = run.QueuedAt
		completed, committed, err := s.CompleteRun(ctx, "agent", run.ID, controlplane.RunCompletion{Run: report})
		require.NoError(t, err)
		require.True(t, committed)
		require.True(t, completed.LeaseUntil.Equal(renewed.LeaseUntil))
		_, _, err = s.CompleteRun(ctx, "agent", run.ID, controlplane.RunCompletion{Run: *completed})
		require.NoError(t, err)
		changed := *completed
		changed.Status = controlplane.RunFailed
		_, _, err = s.CompleteRun(ctx, "agent", run.ID, controlplane.RunCompletion{Run: changed})
		require.ErrorIs(t, err, persistence.ErrConflict)
	})
}

func TestCompletionReceiptRestoresCanonicalProjectionAfterRestart(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		run := controlplane.Run{ID: "receipt", Topology: "lab", Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Hour)}
		require.NoError(t, store.SaveRun(context.Background(), run))
		run.Status = controlplane.RunDone
		report := controlplane.RunCompletion{Run: run, Projection: controlplane.Projection{Serial: 7, ResourceCount: 2}}
		// Crash after durable completion but before the in-memory registry update.
		_, changed, err := store.CompleteRun(context.Background(), "agent", run.ID, report)
		require.NoError(t, err)
		require.True(t, changed)
		registry := newAgentRegistry()
		service := AgentService{store: store, registry: registry}
		report.Projection.Serial = 999
		result, err := service.CompleteRun("agent", run.ID, report)
		require.NoError(t, err)
		require.EqualValues(t, 7, result.Projection.Serial)
		projections := registry.ListProjections("agent")
		require.Len(t, projections, 1)
		require.EqualValues(t, 7, projections[0].Serial)
	})
}

func TestCompletionProjectionLoadedAtAPIStartup(t *testing.T) {
	runs, workspaces := t.TempDir(), t.TempDir()
	server := NewServer(runs, workspaces)
	run := controlplane.Run{ID: "startup", Topology: "lab", Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Hour)}
	require.NoError(t, server.apiStore.SaveRun(context.Background(), run))
	run.Status = controlplane.RunDone
	_, _, err := server.apiStore.CompleteRun(context.Background(), "agent", run.ID, controlplane.RunCompletion{Run: run, Projection: controlplane.Projection{Serial: 7}})
	require.NoError(t, err)
	restarted := NewServer(runs, workspaces)
	require.NoError(t, restarted.initErr)
	projections := restarted.agents.ListProjections("agent")
	require.Len(t, projections, 1)
	require.EqualValues(t, 7, projections[0].Serial)
}

type completionPublicationRace struct {
	apiStore
	publishNewer func()
}

func (s completionPublicationRace) CompleteRun(ctx context.Context, agent, id string, report controlplane.RunCompletion) (*controlplane.Run, bool, error) {
	run, changed, err := s.apiStore.CompleteRun(ctx, agent, id, report)
	if err == nil {
		s.publishNewer()
	}
	return run, changed, err
}
func TestFirstCompletionDoesNotOverwriteNewerObservation(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	run := controlplane.Run{ID: "publication", Topology: "lab", Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Hour)}
	require.NoError(t, store.SaveRun(context.Background(), run))
	registry := newAgentRegistry()
	service := AgentService{registry: registry, store: completionPublicationRace{apiStore: store, publishNewer: func() {
		registry.SaveProjection(controlplane.Projection{AgentID: "agent", Workspace: "lab", Topology: "lab", Serial: 99, UpdatedAt: time.Now().Add(time.Minute)})
	}}}
	run.Status = controlplane.RunDone
	_, err := service.CompleteRun("agent", run.ID, controlplane.RunCompletion{Run: run, Projection: controlplane.Projection{Serial: 7}})
	require.NoError(t, err)
	require.EqualValues(t, 99, registry.ListProjections("agent")[0].Serial)
}
