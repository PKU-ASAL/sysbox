package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

type runStorageFixture struct {
	record    *controlplane.Run
	before    func()
	commitErr error
}

func (s *runStorageFixture) UpdateRun(ctx context.Context, _ string, fn func(*controlplane.Run) (*controlplane.Run, error)) (*controlplane.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.before != nil {
		s.before()
	}
	var current *controlplane.Run
	if s.record != nil {
		copy := *s.record
		current = &copy
	}
	next, err := fn(current)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return current, nil
	}
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	s.record = next
	copy := *next
	return &copy, nil
}
func TestRunRepositoryNeverGrantsLeaseAfterCommitFailure(t *testing.T) {
	now := time.Now().UTC()
	original := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
	want := errors.New("commit unavailable")
	storage := &runStorageFixture{record: &original, commitErr: want}
	repo := NewRunRepository(storage)
	repo.now = func() time.Time { return now }
	got, ok, err := repo.ClaimRun(context.Background(), original.ID, original.AgentID, "owner", time.Minute)
	require.ErrorIs(t, err, want)
	require.False(t, ok)
	require.Nil(t, got)
	require.Equal(t, controlplane.RunAssigned, storage.record.Status)
	require.Zero(t, storage.record.Attempt)
}
func TestRunRepositoryChecksClockAfterAcquiringStorage(t *testing.T) {
	now := time.Now().UTC()
	original := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned, DeadlineAt: now.Add(time.Minute)}
	storage := &runStorageFixture{record: &original}
	repo := NewRunRepository(storage)
	repo.now = func() time.Time { return now }
	storage.before = func() { now = now.Add(2 * time.Minute) }
	_, ok, err := repo.ClaimRun(context.Background(), original.ID, original.AgentID, "owner", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
}
func TestRunRepositoryClaimCannotCrossConcurrentRestart(t *testing.T) {
	original := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
	storage := &runStorageFixture{record: &original}
	ctx := WithRunClaimIdentity(context.Background(), original)
	storage.before = func() { next := original; next.QueuedAt = original.QueuedAt.Add(time.Second); storage.record = &next }
	got, ok, err := NewRunRepository(storage).ClaimRun(ctx, original.ID, original.AgentID, "owner", time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	require.False(t, ok)
	require.Nil(t, got)
	require.Equal(t, controlplane.RunAssigned, storage.record.Status)
}
func TestRunRepositoryCapsLeaseAtDeadline(t *testing.T) {
	now := time.Now().UTC()
	original := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned, DeadlineAt: now.Add(time.Minute)}
	storage := &runStorageFixture{record: &original}
	repo := NewRunRepository(storage)
	repo.now = func() time.Time { return now }
	claimed, ok, err := repo.ClaimRun(context.Background(), original.ID, original.AgentID, "owner", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, original.DeadlineAt, claimed.LeaseUntil)
	now = now.Add(10 * time.Second)
	renewed, ok, err := repo.RenewRunLease(context.Background(), original.ID, original.AgentID, "owner", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, claimed.LeaseUntil, renewed.LeaseUntil)
}

func TestClaimReceiptReplayDoesNotRenewOrChangeAttempt(t *testing.T) {
	now := time.Now().UTC()
	original := controlplane.Run{ID: "replay", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}
	storage := &runStorageFixture{record: &original}
	repo := NewRunRepository(storage)
	repo.now = func() time.Time { return now }
	first, ok, err := repo.ClaimRun(context.Background(), original.ID, "agent", "execution-one", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	now = now.Add(time.Second)
	replay, ok, err := repo.ClaimRun(context.Background(), original.ID, "agent", "execution-one", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first, replay)
	_, ok, err = repo.ClaimRun(context.Background(), original.ID, "agent", "execution-two", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
	now = now.Add(time.Minute)
	_, ok, err = repo.ClaimRun(context.Background(), original.ID, "agent", "execution-one", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRunCompletionProjectionCannotBeInjected(t *testing.T) {
	storage := &runStorageFixture{}
	repo := NewRunRepository(storage)
	run := controlplane.Run{ID: "injected", Topology: "lab", Status: controlplane.RunQueued, CompletionProjection: &controlplane.Projection{Serial: 999}}
	require.ErrorIs(t, repo.SaveRun(context.Background(), run), ErrConflict)
	run.CompletionProjection = nil
	require.NoError(t, repo.SaveRun(context.Background(), run))
	run.CompletionProjection = &controlplane.Projection{Serial: 999}
	require.ErrorIs(t, repo.SaveRun(context.Background(), run), ErrConflict)
}
