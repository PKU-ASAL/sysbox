package runtime

import (
	"context"
	"errors"
	"github.com/hashicorp/hcl/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type convergingFirewallPolicy struct {
	exists      bool
	applies     int
	deletes     int
	observation driver.RulesetObservation
}

func (p *convergingFirewallPolicy) ApplyRuleset(_ context.Context, _ driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	p.applies++
	p.exists = true
	digest, err := driver.RulesetDigest(spec, nil)
	p.observation = driver.RulesetObservation{Table: driver.RulesetTableName(spec.Owner), Digest: digest}
	return p.observation, err
}
func (p *convergingFirewallPolicy) ObserveRuleset(context.Context, driver.PolicyTarget, string) (driver.RulesetObservation, error) {
	if !p.exists {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorNotFound, "test", "absent", nil)
	}
	return p.observation, nil
}
func (p *convergingFirewallPolicy) DiscoverRuleset(ctx context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	return p.ObserveRuleset(ctx, target, spec.Owner)
}
func (p *convergingFirewallPolicy) DeleteRuleset(context.Context, driver.PolicyTarget, string) error {
	p.deletes++
	p.exists = false
	return nil
}

func TestFirewallDefaultEngineCreateObserveAndDestroy(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	policy := &convergingFirewallPolicy{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: policy}))
	a := address.Resource("sysbox_firewall", "edge")
	g := graph.New()
	require.NoError(t, g.AddNode(a, nil))
	require.NoError(t, g.SetData(a, &config.FirewallConfig{AttachTo: "sysbox_router.edge"}))
	st := &state.State{Version: state.SchemaVersion, TopologyUID: "owner"}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_router", "edge"), OwnerUID: "owner", ResourceUID: "router", Generation: 2, Driver: "policy-test", ExternalID: "container"})
	e := NewExecutor(g, st, driver.DefaultRegistry)
	e.SetRunContext("lab", "run")
	stored, err := cloneExecutionState(st)
	require.NoError(t, err)
	e.SetActionRepository(&memoryActions{state: stored})
	plan := &Plan{Actions: []model.PlannedChange{{Address: a, Action: model.PlanActionCreate}}}
	require.NoError(t, e.Apply(context.Background(), plan))
	require.Equal(t, 1, policy.applies)
	require.NotNil(t, st.FindResource(a))
	require.ErrorIs(t, e.Apply(context.Background(), plan), ErrPlanStale)
	require.Equal(t, 1, policy.applies, "stale create plan must not replay apply")
	deletePlan := &Plan{Actions: []model.PlannedChange{{Address: a, Action: model.PlanActionDelete}}}
	require.NoError(t, e.Apply(context.Background(), deletePlan))
	require.Equal(t, 1, policy.deletes)
	require.Nil(t, st.FindResource(a))
}

func TestFirewallRecoveryDiscoversLostOutcomeWithoutReplay(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	policy := &convergingFirewallPolicy{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: policy}))
	a := address.Resource("sysbox_firewall", "edge")
	g := graph.New()
	require.NoError(t, g.AddNode(a, nil))
	require.NoError(t, g.SetData(a, &config.FirewallConfig{AttachTo: "sysbox_router.edge"}))
	st := &state.State{Version: state.SchemaVersion, TopologyUID: "owner"}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_router", "edge"), OwnerUID: "owner", ResourceUID: "router", Generation: 2, Driver: "policy-test", ExternalID: "container"})
	e := NewExecutor(g, st, driver.DefaultRegistry)
	e.SetRunContext("lab", "run")
	stored, err := cloneExecutionState(st)
	require.NoError(t, err)
	repo := &memoryActions{state: stored, outcomeErr: errors.New("lost outcome")}
	e.SetActionRepository(repo)
	plan := &Plan{Actions: []model.PlannedChange{{Address: a, Action: model.PlanActionCreate}}}
	require.Error(t, e.Apply(context.Background(), plan))
	require.Equal(t, 1, policy.applies)
	require.Nil(t, st.FindResource(a))
	require.Len(t, repo.actions, 1)
	require.Nil(t, repo.actions[0].Candidate, "only durable intent survived")
	repo.outcomeErr = nil
	reopened, err := cloneExecutionState(repo.state)
	require.NoError(t, err)
	recovery := NewExecutor(g, reopened, driver.DefaultRegistry)
	recovery.SetRunContext("lab", "recovery")
	recovery.SetActionRepository(repo)
	require.ErrorIs(t, recovery.RecoverActions(context.Background(), plan), ErrActionUncertain)
	require.Nil(t, reopened.FindResource(a))
	require.Equal(t, 1, policy.applies, "recovery must not replay")
}

// freshTopologyNode stands in for sysbox_node so the test can drive a whole
// apply without a real substrate. It creates a managed identity inside the
// target's network namespace, exactly like the real node handler does.
type freshTopologyNode struct{ creates int }

func (*freshTopologyNode) Type() string           { return "sysbox_node" }
func (*freshTopologyNode) Schema() ResourceSchema { return ResourceSchemaFor("sysbox_node") }
func (*freshTopologyNode) Read(_ context.Context, r state.Resource, _ *driver.Registry) (ResourceReadResult, error) {
	status := state.ResourceAbsent
	if r.ExternalID != "" {
		status = state.ResourcePresent
	}
	return ResourceReadResult{Status: status, Resource: r}, nil
}
func (*freshTopologyNode) Discover(context.Context, *ProviderContext, *graph.Node) (ResourceReadResult, error) {
	return ResourceReadResult{Status: state.ResourceAbsent}, nil
}
func (n *freshTopologyNode) PlanDiff(node *graph.Node, current *state.Resource) (model.PlannedChange, error) {
	action := model.PlanActionNoop
	if current == nil {
		action = model.PlanActionCreate
	}
	return model.PlannedChange{Address: node.Address, Action: action}, nil
}
func (n *freshTopologyNode) Create(ctx context.Context, _ *ProviderContext, node *graph.Node) (state.Resource, error) {
	n.creates++
	identity, _ := substrate.OperationIdentityFromContext(ctx)
	return state.Resource{Address: node.Address, Driver: "policy-test", ExternalID: "node-container", OwnerUID: identity.OwnerUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation, Attributes: state.MustAttributes(map[string]any{"container_id": "node-container"})}, nil
}
func (*freshTopologyNode) Delete(_ context.Context, pc *ProviderContext, r state.Resource) error {
	pc.State().RemoveResource(r.Address)
	return nil
}
func (*freshTopologyNode) ExternalID(r state.Resource) string { return r.ExternalID }

// A firewall lives inside its target's namespace, so a fresh topology creates
// both in one apply. The pre-effect observation pass must report the firewall
// as absent, not unknown, while its target is still unapplied.
func TestFirewallDiscoveryOnFreshTopologyCreatesWithItsTarget(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	isolateHandlerRegistry(t)
	policy := &convergingFirewallPolicy{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: policy}))
	node := &freshTopologyNode{}
	RegisterResourceHandler(node)
	RegisterResourceHandler(FirewallResourceHandler{})

	targetAddr := address.Resource("sysbox_node", "app")
	firewallAddr := address.Resource("sysbox_firewall", "app_fw")
	g := graph.New()
	require.NoError(t, g.AddNode(targetAddr, nil))
	require.NoError(t, g.SetData(targetAddr, &config.NodeConfig{}))
	require.NoError(t, g.AddNode(firewallAddr, []address.Address{targetAddr}))
	require.NoError(t, g.SetData(firewallAddr, &config.FirewallConfig{AttachTo: "sysbox_node.app"}))

	st := &state.State{Version: state.SchemaVersion, TopologyUID: "owner"}
	e := NewExecutor(g, st, driver.DefaultRegistry)
	e.SetRunContext("lab", "run")
	stored, err := cloneExecutionState(st)
	require.NoError(t, err)
	e.SetActionRepository(&memoryActions{state: stored})

	plan := &Plan{Actions: []model.PlannedChange{
		{Address: targetAddr, Action: model.PlanActionCreate},
		{Address: firewallAddr, Action: model.PlanActionCreate},
	}}
	require.NoError(t, e.Apply(context.Background(), plan))
	require.Equal(t, 1, node.creates)
	require.Equal(t, 1, policy.applies, "fresh topology must reach the firewall create")
	require.NotNil(t, st.FindResource(firewallAddr))
}

// Reporting an unapplied target as absent must not weaken creation: a firewall
// whose target has no persisted managed identity can never produce an effect.
func TestFirewallCreateWithoutAppliedTargetAppliesNothing(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	policy := &firewallPolicyFake{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: policy}))

	st := &state.State{Version: state.SchemaVersion, TopologyUID: "owner"}
	exec := NewExecutor(graph.New(), st, driver.DefaultRegistry)
	n := &graph.Node{Address: address.Resource("sysbox_firewall", "app_fw"), Data: &config.FirewallConfig{AttachTo: "sysbox_node.app"}}
	_, err := exec.createFirewallResource(substrate.WithOperationIdentity(context.Background(), policyOperationFixture()), n)
	require.ErrorIs(t, err, errPolicyTargetUnapplied)
	require.Equal(t, 0, policy.applyCount)
}

func TestFirewallReadInvalidTargetIsUnknown(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: &convergingFirewallPolicy{}}))
	res := state.Resource{Address: address.Resource("sysbox_firewall", "edge"), Driver: "policy-test", Attributes: state.MustAttributes(map[string]any{"owner": "owner", "policy_target_state": `{"container_id":"legacy"}`})}
	observed, err := (FirewallResourceHandler{}).Read(context.Background(), res, driver.DefaultRegistry)
	require.Error(t, err)
	require.Equal(t, state.ResourceUnknown, observed.Status)
}

func (*freshTopologyNode) DecodeResource(config.ResourceBlock, string, *hcl.EvalContext, *driver.Registry) (any, []address.Address, error) {
	return nil, nil, nil
}

func TestFirewallFreshHCLDependencyAndApply(t *testing.T) {
	isolateHandlerRegistry(t)
	node := &freshTopologyNode{}
	RegisterResourceHandler(node)
	RegisterResourceHandler(FirewallResourceHandler{})
	providers := driver.NewRegistry()
	policy := &convergingFirewallPolicy{}
	require.NoError(t, providers.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: policy}))
	dir := t.TempDir()
	path := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte(`resource "sysbox_firewall" "fw" { attach_to = "sysbox_node.app" }
resource "sysbox_node" "app" {}
`), 0600))
	g, _, st, _, _, err := LoadWorkspaceWithInputs(path, state.NewManager(filepath.Join(dir, "state.json")), nil, providers)
	require.NoError(t, err)
	st.TopologyUID = "owner"
	e := NewExecutor(g, st, providers)
	e.SetRunContext("lab", "run")
	stored, err := cloneExecutionState(st)
	require.NoError(t, err)
	e.SetActionRepository(&memoryActions{state: stored})
	plan, err := ComputePlan(g, st, providers)
	require.NoError(t, err)
	require.NoError(t, e.Apply(context.Background(), plan))
	require.Equal(t, 1, node.creates)
	require.Equal(t, 1, policy.applies)
	require.NotNil(t, st.FindResource(address.Resource("sysbox_firewall", "fw")))
}

func TestFirewallDiscoveryRejectsMalformedPersistedTarget(t *testing.T) {
	providers := driver.NewRegistry()
	policy := &convergingFirewallPolicy{}
	require.NoError(t, providers.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: policy}))
	st := &state.State{Version: state.SchemaVersion, TopologyUID: "owner"}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_node", "app"), Driver: "policy-test", ExternalID: "instance"})
	e := NewExecutor(graph.New(), st, providers)
	n := &graph.Node{Address: address.Resource("sysbox_firewall", "fw"), Data: &config.FirewallConfig{AttachTo: "sysbox_node.app"}}
	result, err := (FirewallResourceHandler{}).Discover(context.Background(), &ProviderContext{exec: e}, n)
	require.ErrorContains(t, err, "lacks durable identity")
	require.NotEqual(t, state.ResourceAbsent, result.Status)
	require.Zero(t, policy.applies)
}

func TestFirewallMissingTargetDoesNotReplayUnfinishedIntent(t *testing.T) {
	providers := driver.NewRegistry()
	policy := &convergingFirewallPolicy{}
	require.NoError(t, providers.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: policy}))
	a := address.Resource("sysbox_firewall", "fw")
	g := graph.New()
	require.NoError(t, g.AddNode(a, nil))
	require.NoError(t, g.SetData(a, &config.FirewallConfig{AttachTo: "sysbox_node.app"}))
	st := &state.State{Version: state.SchemaVersion, TopologyUID: "owner"}
	before := ActionRecord{ID: "pending", Topology: "lab", RunID: "old", Resource: a, Action: model.PlanActionCreate, Status: ActionIntentCommitted, OwnerUID: "owner", ResourceUID: "policy", Generation: 1, ProviderOperationID: "returned-operation", Quiescence: "returned"}
	repo := &memoryActions{state: st, actions: []ActionRecord{before}}
	e := NewExecutor(g, st, providers)
	e.SetRunContext("lab", "new")
	e.SetActionRepository(repo)
	err := e.Apply(context.Background(), &Plan{Actions: []model.PlannedChange{{Address: a, Action: model.PlanActionCreate}}})
	require.ErrorIs(t, err, ErrActionUncertain)
	require.Zero(t, policy.applies)
	require.Equal(t, []ActionRecord{before}, repo.actions)
	require.Empty(t, st.Resources)
}
