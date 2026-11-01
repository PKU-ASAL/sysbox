package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

type convergenceHandler struct {
	status                  state.ResourceStatus
	creates, deletes, reads int
	createErr               error
	afterCreate             state.ResourceStatus
	afterDelete             state.ResourceStatus
	onCreate                func()
	onDelete                func()
}

func (*convergenceHandler) Type() string { return "test_converge" }
func (*convergenceHandler) Schema() ResourceSchema {
	return ResourceSchema{Type: "test_converge", Version: 1}
}
func (h *convergenceHandler) Read(_ context.Context, r state.Resource, providers *driver.Registry) (ResourceReadResult, error) {
	h.reads++
	return ResourceReadResult{Status: h.status, Resource: r}, nil
}
func (h *convergenceHandler) Discover(ctx context.Context, _ *ProviderContext, n *graph.Node) (ResourceReadResult, error) {
	r := state.Resource{Address: n.Address}
	if h.status == state.ResourcePresent {
		r.ExternalID = "instance-1"
	}
	return h.Read(ctx, r, driver.DefaultRegistry)
}
func (*convergenceHandler) PlanDiff(n *graph.Node, r *state.Resource) (model.PlannedChange, error) {
	a := model.PlanActionNoop
	if r == nil {
		a = model.PlanActionCreate
	}
	return model.PlannedChange{Address: n.Address, Action: a}, nil
}
func (h *convergenceHandler) Create(_ context.Context, _ *ProviderContext, n *graph.Node) (state.Resource, error) {
	h.creates++
	h.status = h.afterCreate
	if h.onCreate != nil {
		h.onCreate()
	}
	return state.Resource{Address: n.Address, ExternalID: "instance-1"}, h.createErr
}
func (h *convergenceHandler) Delete(_ context.Context, pc *ProviderContext, r state.Resource) error {
	h.deletes++
	h.status = h.afterDelete
	if h.onDelete != nil {
		h.onDelete()
	}
	pc.State().RemoveResource(r.Address)
	return nil
}
func (*convergenceHandler) ExternalID(r state.Resource) string { return r.ExternalID }
func convergenceFixture(t *testing.T, status state.ResourceStatus, existing bool) (*Executor, *convergenceHandler, address.Address) {
	t.Helper()
	isolateHandlerRegistry(t)
	h := &convergenceHandler{status: status, afterCreate: state.ResourcePresent, afterDelete: state.ResourceAbsent}
	RegisterResourceHandler(h)
	a := address.Resource(h.Type(), "node")
	g := graph.New()
	require.NoError(t, g.AddNode(a, nil))
	st := &state.State{Version: state.SchemaVersion, TopologyUID: "test-uid"}
	if existing {
		st.AddResource(state.Resource{Address: a, ExternalID: "instance-1", OwnerUID: "test-uid", ResourceUID: "test-uid/" + a.String(), Generation: 1})
	}
	e := NewExecutor(g, st, driver.DefaultRegistry)
	e.SetRunContext("test-topology", "run-1")
	stored, err := cloneExecutionState(st)
	require.NoError(t, err)
	e.SetActionRepository(&memoryActions{state: stored})
	return e, h, a
}

type memoryActions struct {
	state                 *state.State
	actions               []ActionRecord
	intentErr, outcomeErr error
	patchErr              error
}

func testActionExecutor(t *testing.T, g *graph.Graph, st *state.State) *Executor {
	t.Helper()
	if st.TopologyUID == "" {
		st.TopologyUID = "test-uid"
	}
	for i := range st.Resources {
		r := &st.Resources[i]
		r.OwnerUID = st.TopologyUID
		r.ResourceUID = st.TopologyUID + "/" + r.Address.String()
		if r.Generation < 1 {
			r.Generation = 1
		}
	}
	stored, err := cloneExecutionState(st)
	require.NoError(t, err)
	e := NewExecutor(g, st, driver.DefaultRegistry)
	e.SetRunContext("test-topology", "run-1")
	e.SetActionRepository(&memoryActions{state: stored})
	return e
}

func (m *memoryActions) LoadActions(context.Context, string) ([]ActionRecord, error) {
	return append([]ActionRecord(nil), m.actions...), nil
}
func (m *memoryActions) LoadState(context.Context, string) (*state.State, error) {
	return cloneExecutionState(m.state)
}
func (m *memoryActions) CommitResetIntents(ctx context.Context, batch []ActionRecord) error {
	staged := *m
	staged.actions = append([]ActionRecord(nil), m.actions...)
	for _, a := range batch {
		if err := staged.CommitActionIntent(ctx, a); err != nil {
			return err
		}
	}
	m.actions = staged.actions
	return nil
}
func (m *memoryActions) CommitActionIntent(ctx context.Context, r ActionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.intentErr != nil {
		return m.intentErr
	}
	for _, old := range m.actions {
		if old.Resource.Equal(r.Resource) && !ActionSettled(old) {
			return errors.New("unresolved action")
		}
	}
	r.Revision++
	m.actions = append(m.actions, r)
	return nil
}
func (m *memoryActions) CommitActionOutcome(ctx context.Context, r ActionRecord, p *StatePatch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.outcomeErr != nil {
		return m.outcomeErr
	}
	if p != nil && m.patchErr != nil {
		return m.patchErr
	}
	for i := range m.actions {
		if m.actions[i].ID == r.ID {
			if m.actions[i].Revision != r.Revision {
				return errors.New("action revision conflict")
			}
			r.Revision++
			m.actions[i] = r
		}
	}
	if p != nil {
		ApplyStatePatch(m.state, *p)
		m.state.Meta.Serial++
	}
	return nil
}

func TestConvergenceRecoveryVerifiesLostCommitWithoutCreatingAgain(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	repo := e.actions.(*memoryActions)
	repo.patchErr = errors.New("commit unavailable")
	require.Error(t, e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate)))
	require.Nil(t, e.state.FindResource(a))
	require.Equal(t, 1, h.creates)
	require.Len(t, repo.actions, 1)
	require.NotNil(t, repo.actions[0].Candidate)
	repo.patchErr = nil
	restarted := NewExecutor(e.graph, e.state, driver.DefaultRegistry)
	restarted.SetRunContext("test-topology", "run-2")
	restarted.SetActionRepository(repo)
	require.NoError(t, restarted.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate)))
	require.Equal(t, 1, h.creates)
	require.NotNil(t, restarted.state.FindResource(a))
	require.Equal(t, ActionSucceeded, repo.actions[0].Status)
}

func TestReturnedDeletionPersistsEvidenceAfterCancellation(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourcePresent, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.onDelete = cancel
	err := e.Destroy(ctx, singleConvergencePlan(a, model.PlanActionDelete))
	require.Error(t, err)
	repo := e.actions.(*memoryActions)
	require.Len(t, repo.actions, 1)
	require.True(t, repo.actions[0].EffectComplete, "returned deletion evidence must survive cancellation")
	require.Equal(t, 1, h.deletes)
	require.NotNil(t, repo.state.FindResource(a), "cancelled execution must not publish cleanup")
	cold := NewExecutor(e.graph, e.state, driver.DefaultRegistry)
	cold.SetRunContext(e.topology, "recovery")
	cold.SetActionRepository(repo)
	require.NoError(t, cold.RecoverActions(context.Background(), singleConvergencePlan(a, model.PlanActionDelete)))
	require.Equal(t, 1, h.deletes)
	require.Nil(t, repo.state.FindResource(a))
}
func TestConvergenceRecoveryUnknownNeverRepeatsCreate(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	h.afterCreate = state.ResourceUnknown
	require.Error(t, e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate)))
	require.Error(t, e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate)))
	require.Equal(t, 1, h.creates)
}
func TestConvergenceIntentFailurePreventsProviderCall(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	want := errors.New("disk full")
	e.actions.(*memoryActions).intentErr = want
	require.ErrorIs(t, e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate)), want)
	require.Zero(t, h.creates)
}
func TestConvergenceCancellationKeepsDurableIntent(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	ctx, cancel := context.WithCancel(context.Background())
	h.onCreate = cancel
	require.ErrorIs(t, e.Apply(ctx, singleConvergencePlan(a, model.PlanActionCreate)), context.Canceled)
	require.Len(t, e.actions.(*memoryActions).actions, 1)
	require.NotEqual(t, ActionSucceeded, e.actions.(*memoryActions).actions[0].Status)
	require.Nil(t, e.state.FindResource(a))
	require.Zero(t, h.deletes)
}
func TestConvergenceHealthyApplyDoesNotRepeatEffects(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	require.NoError(t, e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate)))
	require.NoError(t, e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionNoop)))
	require.Equal(t, 1, h.creates)
	require.Zero(t, h.deletes)
}
func TestConvergenceUncertainProvisionerCannotBeReplayed(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourcePresent, false)
	repo := e.actions.(*memoryActions)
	repo.actions = []ActionRecord{{ID: "pending", Topology: "test-topology", Resource: a, Action: model.PlanActionCreate, Status: ActionUncertain, ReplayForbidden: true}}
	require.ErrorIs(t, e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate)), ErrActionUncertain)
	require.Zero(t, h.creates)
}

func TestConvergenceRecoveryDoesNotReplaceCompletedNewGeneration(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourcePresent, true)
	repo := e.actions.(*memoryActions)
	digest, err := desiredHash(e.graph.Get(a))
	require.NoError(t, err)
	candidate := cloneResetStateResource(*e.state.FindResource(a))
	repo.actions = []ActionRecord{{ID: "pending-new", Topology: "test-topology", OwnerUID: "test-uid", ResourceUID: "test-uid/" + a.String(), Generation: 1, Resource: a, Action: model.PlanActionCreate, Status: ActionUncertain, DesiredDigest: digest, Candidate: &candidate, EffectComplete: true, ProviderOperationID: "pending-new", Quiescence: "returned"}}
	require.NoError(t, e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionReplace)))
	require.Zero(t, h.creates)
	require.Zero(t, h.deletes)
}

func TestConvergencePatchPreservesIdentityAndDependencies(t *testing.T) {
	a := address.Resource("sysbox_node", "node")
	dep := address.Resource("sysbox_network", "lan")
	original := state.Resource{Address: a, OwnerUID: "owner", ResourceUID: "resource", Generation: 3, ResourceType: a.Type, SchemaVersion: 2, ExternalID: "node-id", Dependencies: []address.Address{dep}}
	recovered := StateResourceFromLog(resourceLog(original))
	require.Equal(t, original.OwnerUID, recovered.OwnerUID)
	require.Equal(t, original.ResourceUID, recovered.ResourceUID)
	require.Equal(t, original.Generation, recovered.Generation)
	require.Equal(t, original.Dependencies, recovered.Dependencies)
	require.Equal(t, original.SchemaVersion, recovered.SchemaVersion)
}

type hierarchyHandler struct {
	convergenceHandler
	gone  map[string]bool
	order []string
}

func (h *hierarchyHandler) Read(_ context.Context, r state.Resource, providers *driver.Registry) (ResourceReadResult, error) {
	status := state.ResourcePresent
	if h.gone[r.Address.String()] {
		status = state.ResourceAbsent
	}
	return ResourceReadResult{Status: status, Resource: r}, nil
}
func (h *hierarchyHandler) Delete(_ context.Context, _ *ProviderContext, r state.Resource) error {
	h.order = append(h.order, r.Address.String())
	h.gone[r.Address.String()] = true
	return nil
}
func TestConvergenceDestroyUsesPersistedDependencies(t *testing.T) {
	isolateHandlerRegistry(t)
	h := &hierarchyHandler{gone: map[string]bool{}}
	RegisterResourceHandler(h)
	parent := address.Resource(h.Type(), "parent")
	child := address.Resource(h.Type(), "child")
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: parent, ExternalID: "parent"}, {Address: child, ExternalID: "child", Dependencies: []address.Address{parent}}}}
	e := testActionExecutor(t, graph.New(), st)
	plan := &Plan{Actions: []model.PlannedChange{{Address: parent, Action: model.PlanActionDelete}, {Address: child, Action: model.PlanActionDelete}}}
	require.NoError(t, e.Destroy(context.Background(), plan))
	require.Equal(t, []string{child.String(), parent.String()}, h.order)
}
func TestConvergenceReplacementCannotDestroyDependencyOfUnchangedResource(t *testing.T) {
	isolateHandlerRegistry(t)
	h := &hierarchyHandler{gone: map[string]bool{}}
	RegisterResourceHandler(h)
	parent := address.Resource(h.Type(), "parent")
	child := address.Resource(h.Type(), "child")
	g := graph.New()
	require.NoError(t, g.AddNode(parent, nil))
	require.NoError(t, g.AddNode(child, []address.Address{parent}))
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: parent, ExternalID: "parent"}, {Address: child, ExternalID: "child", Dependencies: []address.Address{parent}}}}
	e := testActionExecutor(t, g, st)
	plan := &Plan{Actions: []model.PlannedChange{{Address: parent, Action: model.PlanActionReplace}, {Address: child, Action: model.PlanActionNoop}}}
	require.ErrorIs(t, e.Apply(context.Background(), plan), ErrPlanStale)
	require.Empty(t, h.order)
}

func TestConvergenceComputedPlanPinsDesiredConfiguration(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	cfg := &config.ImageConfig{Source: "/original-image"}
	require.NoError(t, e.graph.SetData(a, cfg))
	plan, err := ComputePlan(e.graph, e.state, driver.DefaultRegistry)
	require.NoError(t, err)
	cfg.Source = "/changed-image"
	require.ErrorIs(t, e.Apply(context.Background(), plan), ErrPlanStale)
	require.Zero(t, h.creates)
}

func TestConvergenceDeleteMissingStateRequiresObservation(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceUnknown, false)
	require.ErrorIs(t, e.Destroy(context.Background(), singleConvergencePlan(a, model.PlanActionDelete)), ErrObservationUnknown)
	require.Zero(t, h.deletes)
}

func TestRecoveryDoesNotOverwriteOrTombstoneNewerGeneration(t *testing.T) {
	for _, action := range []model.PlanActionType{model.PlanActionCreate, model.PlanActionDelete} {
		t.Run(string(action), func(t *testing.T) {
			e, h, addr := convergenceFixture(t, state.ResourcePresent, true)
			old := cloneResetStateResource(*e.state.FindResource(addr))
			current := e.state.FindResource(addr)
			current.Generation = 2
			current.ExternalID = "new-instance"
			repo := e.actions.(*memoryActions)
			var err error
			repo.state, err = cloneExecutionState(e.state)
			require.NoError(t, err)
			repo.actions = []ActionRecord{{ID: "old", Topology: e.topology, RunID: "old-run", OwnerUID: old.OwnerUID, ResourceUID: old.ResourceUID, Generation: 1, Resource: addr, Action: action, Status: ActionUncertain, Candidate: &old, EffectComplete: true, ProviderOperationID: "old", Quiescence: "returned"}}
			if action == model.PlanActionDelete {
				h.status = state.ResourceAbsent
				err = e.Destroy(context.Background(), singleConvergencePlan(addr, model.PlanActionDelete))
			} else {
				err = e.Apply(context.Background(), singleConvergencePlan(addr, model.PlanActionNoop))
			}
			require.ErrorIs(t, err, ErrObservationUnknown)
			require.Equal(t, "new-instance", e.state.FindResource(addr).ExternalID)
			require.Zero(t, h.deletes)
			require.Zero(t, h.creates)
		})
	}
}
func singleConvergencePlan(a address.Address, action model.PlanActionType) *Plan {
	return &Plan{Actions: []model.PlannedChange{{Address: a, Action: action}}}
}
func TestConvergenceUnknownBlocksAllActions(t *testing.T) {
	for _, action := range []model.PlanActionType{model.PlanActionNoop, model.PlanActionCreate, model.PlanActionReplace, model.PlanActionDelete} {
		t.Run(string(action), func(t *testing.T) {
			e, h, a := convergenceFixture(t, state.ResourceUnknown, action != model.PlanActionCreate)
			err := e.Apply(context.Background(), singleConvergencePlan(a, action))
			require.Error(t, err)
			require.Zero(t, h.creates)
			require.Zero(t, h.deletes)
		})
	}
}
func TestConvergenceCreateMustVerifyBeforeStateCommit(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	h.afterCreate = state.ResourceUnknown
	err := e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate))
	require.Error(t, err)
	require.Nil(t, e.state.FindResource(a))
	require.Equal(t, 1, h.creates)
	require.GreaterOrEqual(t, h.reads, 2)
}
func TestConvergenceDeleteMustVerifyBeforeStateRemoval(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourcePresent, true)
	h.afterDelete = state.ResourcePresent
	err := e.Destroy(context.Background(), singleConvergencePlan(a, model.PlanActionDelete))
	require.Error(t, err)
	require.NotNil(t, e.state.FindResource(a))
	require.Equal(t, 1, h.deletes)
}
func TestConvergenceNoopDriftCannotAuthorizeReplacement(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, true)
	err := e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionNoop))
	require.Error(t, err)
	require.Zero(t, h.creates)
	require.Zero(t, h.deletes)
}
func TestConvergenceLostCreateResponseNeverRollsBack(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	h.createErr = errors.New("response lost")
	err := e.Apply(context.Background(), singleConvergencePlan(a, model.PlanActionCreate))
	require.Error(t, err)
	require.Zero(t, h.deletes)
	require.Nil(t, e.state.FindResource(a))
}
