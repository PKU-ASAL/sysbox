package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

type journalResource struct {
	creates, deletes int
	removed          map[string]bool
}

func (*journalResource) Type() string { return "journal_fixture" }
func (*journalResource) Schema() ResourceSchema {
	return ResourceSchema{Type: "journal_fixture", Version: 1}
}
func (p *journalResource) Read(_ context.Context, current state.Resource, providers *driver.Registry) (ResourceReadResult, error) {
	if p.removed[current.Address.String()] {
		return ResourceReadResult{Status: state.ResourceAbsent}, nil
	}
	return resourceReadOK(current), nil
}
func (*journalResource) Discover(context.Context, *ProviderContext, *graph.Node) (ResourceReadResult, error) {
	return ResourceReadResult{Status: state.ResourceAbsent}, nil
}
func (*journalResource) PlanDiff(n *graph.Node, _ *state.Resource) (model.PlannedChange, error) {
	return model.PlannedChange{Address: n.Address, Action: model.PlanActionCreate}, nil
}
func (p *journalResource) Create(_ context.Context, _ *ProviderContext, n *graph.Node) (state.Resource, error) {
	p.creates++
	return state.Resource{Address: n.Address, ExternalID: "owned-node"}, nil
}
func (p *journalResource) Delete(_ context.Context, _ *ProviderContext, r state.Resource) error {
	p.deletes++
	if p.removed == nil {
		p.removed = map[string]bool{}
	}
	p.removed[r.Address.String()] = true
	return nil
}
func (*journalResource) ExternalID(r state.Resource) string { return r.ExternalID }

func journalExecutor(t *testing.T) (*Executor, *journalResource, address.Address) {
	t.Helper()
	previous := resourceHandlers
	resourceHandlers = newHandlerRegistry()
	t.Cleanup(func() { resourceHandlers = previous })
	handler := &journalResource{}
	require.NoError(t, resourceHandlers.Register(handler))
	g := graph.New()
	addr := address.Resource(handler.Type(), "node")
	require.NoError(t, g.AddNode(addr, nil))
	return testActionExecutor(t, g, &state.State{Version: state.SchemaVersion}), handler, addr
}

func TestApplyNeverCreatesWhenIntentPersistenceFails(t *testing.T) {
	for _, failAfter := range []int{1, 2} {
		t.Run(string(rune('0'+failAfter)), func(t *testing.T) {
			executor, handler, addr := journalExecutor(t)
			want := errors.New("intent storage unavailable")
			path := filepath.Join(t.TempDir(), "checkpoint.json")
			recorder := NewStoreRecorder(NewFileRecorder(path, "run", "lab"), &stubCheckpointStore{err: want, failAfter: failAfter}, "lab", "run", path)
			executor.SetRecorder(recorder)
			err := executor.Apply(context.Background(), &Plan{Actions: []model.PlannedChange{{Address: addr, Action: model.PlanActionCreate}}})
			require.ErrorIs(t, err, want)
			require.Zero(t, handler.creates, "provider must not run before durable intent")
		})
	}
}

func TestDestroyNeverDeletesWhenIntentPersistenceFails(t *testing.T) {
	executor, handler, addr := journalExecutor(t)
	executor.state.AddResource(state.Resource{Address: addr, ExternalID: "owned-node"})
	want := errors.New("intent storage unavailable")
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	executor.SetRecorder(NewStoreRecorder(NewFileRecorder(path, "run", "lab"), &stubCheckpointStore{err: want, failAfter: 1}, "lab", "run", path))
	err := executor.Destroy(context.Background(), &Plan{Actions: []model.PlannedChange{{Address: addr, Action: model.PlanActionDelete}}})
	require.ErrorIs(t, err, want)
	require.Zero(t, handler.deletes)
}

type failNextResourceJournal struct {
	count int
	err   error
}

func (s *failNextResourceJournal) SaveCheckpoint(_ context.Context, _, _ string, checkpoint OperationCheckpoint) error {
	for _, step := range checkpoint.Steps {
		if step.Resource == "journal_fixture.second" {
			return s.err
		}
	}
	s.count++
	return nil
}

func TestJournalFailureDoesNotRollbackAlreadyCreatedResource(t *testing.T) {
	executor, handler, first := journalExecutor(t)
	second := address.Resource(handler.Type(), "second")
	require.NoError(t, executor.graph.AddNode(second, nil))
	want := errors.New("journal failed before second resource")
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	executor.SetRecorder(NewStoreRecorder(NewFileRecorder(path, "run", "lab"), &failNextResourceJournal{err: want}, "lab", "run", path))
	err := executor.Apply(context.Background(), &Plan{Actions: []model.PlannedChange{{Address: first, Action: model.PlanActionCreate}, {Address: second, Action: model.PlanActionCreate}}})
	require.ErrorIs(t, err, want)
	require.Equal(t, 1, handler.creates)
	require.Zero(t, handler.deletes, "journal failure must not trigger unrecorded rollback")
	require.NotNil(t, executor.state.FindResource(first))
}

func TestSubstepDoesNotMutateAfterLocalJournalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	local := NewFileRecorder(path, "run", "lab")
	require.NoError(t, local.Begin("apply", nil))
	// Keep the last checkpoint readable, but make the next atomic rename fail.
	require.NoError(t, os.Mkdir(path+".tmp", 0700))
	executor := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion}, driver.DefaultRegistry)
	executor.SetRecorder(NewStoreRecorder(local, nil, "lab", "run", path))
	mutated := false
	err := executor.recordSubstep(-1, "create_node", nil, func() error { mutated = true; return nil })
	require.Error(t, err)
	require.False(t, mutated)
}

type failingStatePatch struct{ err error }

func (s failingStatePatch) ApplyStatePatch(context.Context, StatePatch) error { return s.err }

func TestDestroyStopsAfterStateCommitFails(t *testing.T) {
	executor, handler, first := journalExecutor(t)
	second := address.Resource(handler.Type(), "second")
	executor.state.AddResource(state.Resource{Address: first})
	executor.state.AddResource(state.Resource{Address: second})
	want := errors.New("state commit failed")
	executor = testActionExecutor(t, executor.graph, executor.state)
	executor.actions.(*memoryActions).patchErr = want
	err := executor.Destroy(context.Background(), &Plan{Actions: []model.PlannedChange{{Address: first, Action: model.PlanActionDelete}, {Address: second, Action: model.PlanActionDelete}}})
	require.ErrorIs(t, err, want)
	require.Equal(t, 1, handler.deletes)
}

type failFinishedJournal struct{ err error }

func (s failFinishedJournal) SaveCheckpoint(_ context.Context, _, _ string, checkpoint OperationCheckpoint) error {
	if checkpoint.Status == OperationDone {
		return s.err
	}
	return nil
}

func TestApplyReturnsFinalJournalFailure(t *testing.T) {
	executor, _, _ := journalExecutor(t)
	want := errors.New("final journal commit failed")
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	executor.SetRecorder(NewStoreRecorder(NewFileRecorder(path, "run", "lab"), failFinishedJournal{err: want}, "lab", "run", path))
	require.ErrorIs(t, executor.Apply(context.Background(), &Plan{}), want)
}

type failOneCompletionJournal struct {
	failed bool
	last   OperationCheckpoint
	err    error
}

func (s *failOneCompletionJournal) SaveCheckpoint(_ context.Context, _, _ string, checkpoint OperationCheckpoint) error {
	if !s.failed && len(checkpoint.Steps) > 0 && checkpoint.Steps[0].Status == OperationDone {
		s.failed = true
		return s.err
	}
	s.last = checkpoint
	return nil
}

func TestFinishJournalCannotPersistSuccessAfterTransientStepFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	want := errors.New("last step commit rejected")
	store := &failOneCompletionJournal{err: want}
	recorder := NewStoreRecorder(NewFileRecorder(path, "run", "lab"), store, "lab", "run", path)
	executor := NewExecutor(graph.New(), &state.State{}, driver.DefaultRegistry)
	executor.SetRecorder(recorder)
	require.NoError(t, recorder.Begin("reset", &Plan{}))
	recorder.StepDone(recorder.StepStart("sysbox_node.web", model.PlanActionReset))
	require.ErrorIs(t, executor.finishJournal(nil), want)
	require.Equal(t, OperationFailed, store.last.Status, "sticky failure must be incorporated before Finish")
}
