package runtime

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
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
	e := NewExecutor(g, st)
	e.SetRunContext("lab", "run")
	stored, err := cloneExecutionState(st)
	require.NoError(t, err)
	e.SetActionRepository(&memoryActions{state: stored})
	plan := &Plan{Actions: []controlplane.PlannedChange{{Address: a, Action: controlplane.PlanActionCreate}}}
	require.NoError(t, e.Apply(context.Background(), plan))
	require.Equal(t, 1, policy.applies)
	require.NotNil(t, st.FindResource(a))
	require.ErrorIs(t, e.Apply(context.Background(), plan), ErrPlanStale)
	require.Equal(t, 1, policy.applies, "stale create plan must not replay apply")
	deletePlan := &Plan{Actions: []controlplane.PlannedChange{{Address: a, Action: controlplane.PlanActionDelete}}}
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
	e := NewExecutor(g, st)
	e.SetRunContext("lab", "run")
	stored, err := cloneExecutionState(st)
	require.NoError(t, err)
	repo := &memoryActions{state: stored, outcomeErr: errors.New("lost outcome")}
	e.SetActionRepository(repo)
	plan := &Plan{Actions: []controlplane.PlannedChange{{Address: a, Action: controlplane.PlanActionCreate}}}
	require.Error(t, e.Apply(context.Background(), plan))
	require.Equal(t, 1, policy.applies)
	require.Nil(t, st.FindResource(a))
	require.Len(t, repo.actions, 1)
	require.Nil(t, repo.actions[0].Candidate, "only durable intent survived")
	repo.outcomeErr = nil
	reopened, err := cloneExecutionState(repo.state)
	require.NoError(t, err)
	recovery := NewExecutor(g, reopened)
	recovery.SetRunContext("lab", "recovery")
	recovery.SetActionRepository(repo)
	require.NoError(t, recovery.RecoverActions(context.Background(), plan))
	require.NotNil(t, reopened.FindResource(a))
	require.Equal(t, 1, policy.applies, "recovery must only observe")
}

func TestFirewallReadInvalidTargetIsUnknown(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: &convergingFirewallPolicy{}}))
	res := state.Resource{Address: address.Resource("sysbox_firewall", "edge"), Driver: "policy-test", Attributes: state.MustAttributes(map[string]any{"owner": "owner", "policy_target_state": `{"container_id":"legacy"}`})}
	observed, err := (FirewallResourceHandler{}).Read(context.Background(), res)
	require.Error(t, err)
	require.Equal(t, state.ResourceUnknown, observed.Status)
}
