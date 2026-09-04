package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

type stubCheckpointStore struct {
	saves int
	err   error
	// failAfter lets the store succeed for the first N saves and fail from then
	// on, modelling a backend that breaks partway through a run rather than one
	// that was never reachable.
	failAfter int
}

func (s *stubCheckpointStore) SaveCheckpoint(context.Context, string, string, OperationCheckpoint) error {
	s.saves++
	if s.err != nil && s.saves > s.failAfter {
		return s.err
	}
	return nil
}

// A run whose checkpoint cannot be persisted has no journal, and the journal is
// the only thing that lets a crashed apply be reconciled afterwards: resources
// created but not yet recorded in state are found by replaying it. Losing it
// silently is the worst shape of this failure — the apply goes on and reports
// success, and the gap is discovered only on the day a recovery is attempted.
//
// The recorder must therefore remember that persistence broke, so the run can
// be failed instead of completing on an unrecorded journal.
func TestStoreRecorderReportsUnreadableCheckpointFile(t *testing.T) {
	store := &stubCheckpointStore{}
	// A path that cannot be read: the file was never created.
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	rec := NewStoreRecorder(NoopRecorder{}, store, "lab", "run-1", missing)

	rec.StepDone(0)

	require.Error(t, rec.Err(), "an unreadable checkpoint file must not be swallowed")
	require.Equal(t, 0, store.saves, "nothing should have been persisted")
}

// The same applies one layer out: the file is fine but the store rejects the
// write partway through a run. Today this only reaches stderr, which nothing
// acts on, so the run completes on a journal that stopped being written.
func TestStoreRecorderReportsFailingCheckpointStore(t *testing.T) {
	saveErr := errors.New("checkpoint backend unavailable")
	store := &stubCheckpointStore{err: saveErr, failAfter: 1}
	path := filepath.Join(t.TempDir(), "cp.json")
	inner := NewFileRecorder(path, "run-1", "lab")
	rec := NewStoreRecorder(inner, store, "lab", "run-1", path)

	require.NoError(t, rec.Begin("apply", &Plan{}), "the journal was still healthy at Begin")
	rec.StepDone(rec.StepStart("sysbox_node.web", controlplane.PlanActionCreate))

	require.ErrorIs(t, rec.Err(), saveErr)
}

// A healthy recorder must stay clean, or the check would fail every run.
func TestStoreRecorderStaysCleanWhenPersistenceWorks(t *testing.T) {
	store := &stubCheckpointStore{}
	path := filepath.Join(t.TempDir(), "cp.json")
	inner := NewFileRecorder(path, "run-1", "lab")
	rec := NewStoreRecorder(inner, store, "lab", "run-1", path)

	require.NoError(t, rec.Begin("apply", &Plan{}))
	rec.StepDone(rec.StepStart("sysbox_node.web", controlplane.PlanActionCreate))

	require.NoError(t, rec.Err())
	require.Greater(t, store.saves, 0, "the healthy path must actually persist")
}

// Begin is the one recorder method that can refuse, so a journal that is broken
// from the start should stop the run before any resource is touched rather than
// after.
func TestStoreRecorderBeginFailsWhenPersistenceIsBroken(t *testing.T) {
	store := &stubCheckpointStore{err: errors.New("checkpoint backend unavailable")}
	path := filepath.Join(t.TempDir(), "cp.json")
	inner := NewFileRecorder(path, "run-1", "lab")
	rec := NewStoreRecorder(inner, store, "lab", "run-1", path)

	err := rec.Begin("apply", &Plan{})

	require.Error(t, err, "Begin must refuse when the journal cannot be persisted")
}

// A recorder with no store configured is a legitimate mode (local runs without
// a control plane) and must not be reported as broken.
func TestStoreRecorderWithoutStoreIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	rec := NewStoreRecorder(NewFileRecorder(path, "run-1", "lab"), nil, "lab", "run-1", path)

	require.NoError(t, rec.Begin("apply", &Plan{}))
	rec.StepDone(rec.StepStart("sysbox_node.web", controlplane.PlanActionCreate))

	require.NoError(t, rec.Err())
}
