package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Provider effects are controllable; persistence is the real SQLite adapter.
// No API Server, Agent identity or HTTP listener participates in this fixture.
type operationResource struct {
	kind             string
	exists, unknown  bool
	creates, deletes int
}

func (h *operationResource) Type() string { return h.kind }
func (h *operationResource) Schema() runtime.ResourceSchema {
	return runtime.ResourceSchema{Type: h.kind, Version: 1}
}
func (h *operationResource) Read(_ context.Context, r state.Resource, providers *driver.Registry) (runtime.ResourceReadResult, error) {
	status := state.ResourceAbsent
	if h.exists {
		status = state.ResourcePresent
	}
	if h.unknown {
		status = state.ResourceUnknown
	}
	return runtime.ResourceReadResult{Status: status, Resource: r}, nil
}
func (h *operationResource) Discover(ctx context.Context, _ *runtime.ProviderContext, n *graph.Node) (runtime.ResourceReadResult, error) {
	return h.Read(ctx, state.Resource{Address: n.Address}, driver.DefaultRegistry)
}
func (h *operationResource) PlanDiff(n *graph.Node, r *state.Resource) (model.PlannedChange, error) {
	action := model.PlanActionNoop
	if r == nil {
		action = model.PlanActionCreate
	}
	return model.PlannedChange{Address: n.Address, Action: action}, nil
}
func (h *operationResource) Create(_ context.Context, _ *runtime.ProviderContext, n *graph.Node) (state.Resource, error) {
	h.exists = true
	h.creates++
	return state.Resource{Address: n.Address, ExternalID: "owned-instance"}, nil
}
func (h *operationResource) Delete(context.Context, *runtime.ProviderContext, state.Resource) error {
	h.exists = false
	h.deletes++
	return nil
}
func (h *operationResource) ExternalID(r state.Resource) string { return r.ExternalID }
func (h *operationResource) DecodeResource(config.ResourceBlock, string, *hcl.EvalContext, *driver.Registry) (any, []address.Address, error) {
	return nil, nil, nil
}

var operationResourceID atomic.Uint64

func operationFixture(t *testing.T) (runtime.OperationDependencies, runtime.OperationRequest, *operationResource) {
	t.Helper()
	dir := t.TempDir()
	h := &operationResource{kind: fmt.Sprintf("operation_resource_%d", operationResourceID.Add(1))}
	runtime.RegisterResourceHandler(h)
	path := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`variable "mode" { default = "dev" }
variable "password" { sensitive = true }
resource %q "node" {}
`, h.kind)), 0600))
	repo, err := persistence.OpenConfigured(context.Background(), "sqlite://"+filepath.Join(dir, "state.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repo.Close()) })
	manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
	return runtime.OperationDependencies{Providers: driver.NewRegistry(), Manager: manager, Actions: repo, Log: io.Discard}, runtime.OperationRequest{ID: "first", Topology: "lab", Kind: "apply", Workspace: path, QueuedAt: time.Now().UTC(), Revision: "revision", Inputs: map[string]string{"mode": "prod", "password": "secret-canary"}}, h
}

func TestResourceOperationSQLiteLifecycleWithoutControlPlane(t *testing.T) {
	deps, req, h := operationFixture(t)
	for _, id := range []string{"first", "second"} {
		req.ID = id
		op, err := runtime.PrepareOperation(context.Background(), req, deps)
		require.NoError(t, err)
		plan, err := op.Plan(context.Background())
		require.NoError(t, err)
		require.Equal(t, id == "first", plan.HasChanges())
		require.NoError(t, op.Execute(context.Background(), plan))
		stored, err := deps.Manager.Load()
		require.NoError(t, err)
		require.Equal(t, map[string]string{"mode": "prod"}, stored.AppliedVariables)
		require.Len(t, stored.Resources, 1)
	}
	require.Equal(t, 1, h.creates)
	// Destruction needs canonical state, not the old HCL file or secret input.
	require.NoError(t, os.Remove(req.Workspace))
	req.ID = "destroy"
	req.Kind = "destroy"
	req.Inputs = nil
	op, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err := op.Plan(context.Background())
	require.NoError(t, err)
	require.NoError(t, op.Execute(context.Background(), plan))
	require.Equal(t, 1, h.deletes)
	stored, err := deps.Manager.Load()
	require.NoError(t, err)
	require.Empty(t, stored.Resources)
}

type operationFaultRepository struct {
	runtime.ActionRepository
	failIntent, failOutcome bool
}

func (r *operationFaultRepository) CommitActionIntent(ctx context.Context, a runtime.ActionRecord) error {
	if r.failIntent {
		return errors.New("intent unavailable")
	}
	return r.ActionRepository.CommitActionIntent(ctx, a)
}
func (r *operationFaultRepository) CommitActionOutcome(ctx context.Context, a runtime.ActionRecord, p *runtime.StatePatch) error {
	if r.failOutcome && p != nil {
		return errors.New("result unavailable")
	}
	return r.ActionRepository.CommitActionOutcome(ctx, a, p)
}
func TestResourceOperationRetainsUncertainEffectAfterCommitFailure(t *testing.T) {
	deps, req, h := operationFixture(t)
	fault := &operationFaultRepository{ActionRepository: deps.Actions, failOutcome: true}
	deps.Actions = fault
	op, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err := op.Plan(context.Background())
	require.NoError(t, err)
	require.ErrorContains(t, op.Execute(context.Background(), plan), "result unavailable")
	require.Equal(t, 1, h.creates)
	h.unknown = true
	fault.failOutcome = false
	req.ID = "retry"
	retry, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err = retry.Plan(context.Background())
	require.NoError(t, err)
	require.Error(t, retry.Execute(context.Background(), plan))
	require.Equal(t, 1, h.creates)
	req.Kind = "destroy"
	req.ID = "destroy"
	destroy, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err = destroy.Plan(context.Background())
	require.NoError(t, err)
	require.Error(t, destroy.Execute(context.Background(), plan))
	require.Zero(t, h.deletes)
}
func TestResourceOperationRejectsEffectsBeforeDurableIntent(t *testing.T) {
	deps, req, h := operationFixture(t)
	deps.Actions = &operationFaultRepository{ActionRepository: deps.Actions, failIntent: true}
	op, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err := op.Plan(context.Background())
	require.NoError(t, err)
	require.ErrorContains(t, op.Execute(context.Background(), plan), "intent unavailable")
	require.Zero(t, h.creates)
}
func TestResourceOperationRejectsStalePlan(t *testing.T) {
	deps, req, h := operationFixture(t)
	op, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err := op.Plan(context.Background())
	require.NoError(t, err)
	other, err := deps.Manager.Load()
	require.NoError(t, err)
	require.NoError(t, deps.Manager.Save(other))
	require.ErrorIs(t, op.Execute(context.Background(), plan), runtime.ErrPlanStale)
	require.Zero(t, h.creates)
}

func TestResourceOperationApplyTargetStaysInsidePlannedScope(t *testing.T) {
	deps, req, handler := operationFixture(t)
	raw, err := os.ReadFile(req.Workspace)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(req.Workspace, append(raw, []byte(fmt.Sprintf("\nresource %q %q {}\n", handler.kind, "other"))...), 0600))
	req.ID = "targeted"
	req.Target = handler.kind + ".node"
	op, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err := op.Plan(context.Background())
	require.NoError(t, err)
	require.NoError(t, op.Execute(context.Background(), plan))
	stored, err := deps.Manager.Load()
	require.NoError(t, err)
	require.Len(t, stored.Resources, 1)
	require.Equal(t, "node", stored.Resources[0].Address.Name)
	require.Equal(t, 1, handler.creates)
}

// Same provider name can refer to independent clients without mutating the
// process registry. Prepare both operations before execution to catch late reads.
type operationArtifact struct{ id string }

func (p operationArtifact) ResolveImage(_ context.Context, source substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	return substrate.ArtifactHandle{ID: p.id, Identity: substrate.ArtifactIdentity{Kind: source.Kind, Source: source.Source, Digest: "sha256:" + strings.Repeat("a", 64), Architecture: source.Architecture, GuestFamily: source.GuestFamily}}, nil
}
func TestResourceOperationsUseIndependentProviderRegistries(t *testing.T) {
	const configuration = `resource "sysbox_image" "base" {
 substrate = "isolated"
 kind = "oci"
 source = "fixture"
 architecture = "amd64"
 guest_family = "linux"
}`
	var operations []*runtime.Operation
	for _, id := range []string{"provider-one", "provider-two"} {
		deps, req, _ := operationFixture(t)
		require.NoError(t, os.WriteFile(req.Workspace, []byte(configuration), 0600))
		require.NoError(t, deps.Providers.Register(driver.Descriptor{Name: "isolated", Version: "1", Artifact: operationArtifact{id: id}}))
		req.Inputs = nil
		op, err := runtime.PrepareOperation(context.Background(), req, deps)
		require.NoError(t, err)
		operations = append(operations, op)
	}
	for i, op := range operations {
		t.Run(fmt.Sprintf("operation-%d", i), func(t *testing.T) {
			t.Parallel()
			plan, err := op.Plan(context.Background())
			require.NoError(t, err)
			require.NoError(t, op.Execute(context.Background(), plan))
			require.Equal(t, []string{"provider-one", "provider-two"}[i], op.State.Resources[0].ExternalID)
		})
	}
}
func TestResourceOperationRequiresExplicitProviderRegistry(t *testing.T) {
	deps, req, _ := operationFixture(t)
	deps.Providers = nil
	_, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.ErrorIs(t, err, runtime.ErrConvergenceUnsupported)
}

func TestResourceDestroyPlanRejectsProtectedResource(t *testing.T) {
	stored := &state.State{Resources: []state.Resource{
		{Address: address.Resource("sysbox_node", "web")},
		{Address: address.Resource("sysbox_node", "db"), Attributes: state.MustAttributes(map[string]any{"lifecycle_prevent_destroy": true})},
	}}
	_, err := runtime.BuildDestroyPlan(stored)
	require.ErrorContains(t, err, "prevent_destroy blocks deletion")
}

func TestResourceOperationCancelledBeforeExecutionHasNoEffects(t *testing.T) {
	deps, request, provider := operationFixture(t)
	op, err := runtime.PrepareOperation(context.Background(), request, deps)
	require.NoError(t, err)
	plan, err := op.Plan(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, op.Execute(ctx, plan), context.Canceled)
	require.Zero(t, provider.creates)
	journal, err := deps.Actions.LoadActions(context.Background(), request.Topology)
	require.NoError(t, err)
	require.Empty(t, journal)
}

func TestResourceOperationPlansRepairOfExternallyMissingResource(t *testing.T) {
	deps, req, h := operationFixture(t)
	op, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err := op.Plan(context.Background())
	require.NoError(t, err)
	require.NoError(t, op.Execute(context.Background(), plan))
	h.exists = false
	req.ID = "repair-missing"
	op, err = runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err = op.Plan(context.Background())
	require.NoError(t, err)
	require.Equal(t, model.PlanActionReplace, plan.Actions[0].Action)
	require.NoError(t, op.Execute(context.Background(), plan))
	require.Equal(t, 2, h.creates)
	require.True(t, h.exists)
}

func TestResourceOperationPlanningUnknownPreservesCanonicalState(t *testing.T) {
	deps, req, h := operationFixture(t)
	op, err := runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	plan, err := op.Plan(context.Background())
	require.NoError(t, err)
	require.NoError(t, op.Execute(context.Background(), plan))
	before, err := deps.Manager.Load()
	require.NoError(t, err)
	h.unknown = true
	req.ID = "observe-unknown"
	op, err = runtime.PrepareOperation(context.Background(), req, deps)
	require.NoError(t, err)
	_, err = op.Plan(context.Background())
	require.ErrorIs(t, err, runtime.ErrObservationUnknown)
	after, err := deps.Manager.Load()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, 1, h.creates)
	require.Zero(t, h.deletes)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = op.Plan(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
