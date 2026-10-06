package runtime

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type firewallPolicyFake struct {
	applied    driver.RulesetSpec
	applyCount int
	digest     string
	observeErr error
}

func policyTargetFixture(t *testing.T) string {
	t.Helper()
	raw, err := driver.EncodePolicyTarget(driver.PolicyTarget{Resource: "sysbox_router.edge", Policy: policyOperationFixture(), Identity: substrate.OperationIdentity{ActionID: "test", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "router", Generation: 2}, State: json.RawMessage(`{"container_id":"router","bindings":{}}`)})
	require.NoError(t, err)
	return string(raw)
}

func policyOperationFixture() substrate.OperationIdentity {
	return substrate.OperationIdentity{ActionID: "policy", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "firewall", Generation: 1}
}

func (f *firewallPolicyFake) ApplyRuleset(_ context.Context, _ driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	f.applied = spec
	f.applyCount++
	return driver.RulesetObservation{Table: "sysbox_owned", Digest: "verified"}, nil
}
func (f *firewallPolicyFake) ObserveRuleset(context.Context, driver.PolicyTarget, string) (driver.RulesetObservation, error) {
	if f.observeErr != nil {
		return driver.RulesetObservation{}, f.observeErr
	}
	return driver.RulesetObservation{Table: "sysbox_owned", Digest: f.digest}, nil
}

func (*firewallPolicyFake) DeleteRuleset(context.Context, driver.PolicyTarget, string) error {
	return nil
}

func TestFirewallRulesetRejectsIPv6AndDefaultsDrop(t *testing.T) {
	_, err := firewallRuleset("owner", &config.FirewallConfig{Family: "ipv6"})
	require.ErrorContains(t, err, "only IPv4")
	spec, err := firewallRuleset("owner", &config.FirewallConfig{})
	require.NoError(t, err)
	require.Equal(t, driver.VerdictDrop, spec.DefaultInput)
	require.Equal(t, driver.VerdictDrop, spec.DefaultOutput)
	require.Equal(t, driver.VerdictDrop, spec.DefaultForward)
}

func TestCreateFirewallPersistsVerifiedObservation(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	fake := &firewallPolicyFake{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: fake}))
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_router", "edge"), OwnerUID: "owner", ResourceUID: "router", Generation: 2, Driver: "policy-test", Attributes: state.MustAttributes(map[string]any{"container_id": "router"}), Attachments: []state.Attachment{{Name: "inside", Observation: state.AttachmentObservation{GuestDevice: "eth1"}}}})
	exec := NewExecutor(graph.New(), st)
	n := &graph.Node{Address: address.Resource("sysbox_firewall", "edge"), Data: &config.FirewallConfig{AttachTo: "sysbox_router.edge", Rules: []config.FirewallRule{{Name: "allow", Direction: "forward", Protocol: "all", InputAttachment: "inside", Verdict: "accept"}}}}
	res, err := exec.createFirewallResource(substrate.WithOperationIdentity(context.Background(), policyOperationFixture()), n)
	require.NoError(t, err)
	require.Equal(t, "sysbox_owned", res.Str("table"))
	require.Equal(t, "verified", res.Str("desired_digest"))
	require.Equal(t, "inside", fake.applied.Rules[0].InputAttachment)
	var target map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Str("policy_target_state")), &target))
	require.Equal(t, "router", target["state"].(map[string]any)["container_id"])
	require.NotNil(t, target["identity"], "target ownership must survive persistence")
	identity := target["identity"].(map[string]any)
	require.Equal(t, "owner", identity["owner_uid"])
	require.Equal(t, "router", identity["resource_uid"])
	require.EqualValues(t, 2, identity["generation"])
	policyIdentity := target["policy"].(map[string]any)
	require.Equal(t, "firewall", policyIdentity["resource_uid"])
	require.EqualValues(t, 1, policyIdentity["generation"], "policy and node have independent generations")
	st.FindResource(address.Resource("sysbox_router", "edge")).OwnerUID = ""
	_, err = exec.createFirewallResource(substrate.WithOperationIdentity(context.Background(), policyOperationFixture()), n)
	require.ErrorContains(t, err, "target lacks durable identity")
	require.Equal(t, 1, fake.applyCount, "invalid target must not apply policy")
}

func TestCreateFirewallDelegatesMissingDeviceResolutionToPolicyDriver(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	fake := &firewallPolicyFake{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-test", Version: "1", Policy: fake}))
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_router", "edge"), OwnerUID: "owner", ResourceUID: "router", Generation: 2, Driver: "policy-test", Attributes: state.MustAttributes(map[string]any{"container_id": "router"}), Attachments: []state.Attachment{{Name: "uplink", IPPrefixes: []string{"172.31.42.2/24"}}}})
	exec := NewExecutor(graph.New(), st)
	n := &graph.Node{Address: address.Resource("sysbox_firewall", "edge"), Data: &config.FirewallConfig{AttachTo: "sysbox_router.edge", Rules: []config.FirewallRule{{Name: "allow", Direction: "forward", Protocol: "all", OutputAttachment: "uplink", Verdict: "accept"}}}}

	res, err := exec.createFirewallResource(substrate.WithOperationIdentity(context.Background(), policyOperationFixture()), n)
	require.NoError(t, err)
	var target struct {
		Bindings      map[string]string   `json:"bindings"`
		AttachmentIPs map[string][]string `json:"attachment_ips"`
	}
	persisted, err := driver.DecodePolicyTarget(json.RawMessage(res.Str("policy_target_state")))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(persisted.State, &target))
	require.Empty(t, target.Bindings["uplink"])
	require.Equal(t, []string{"172.31.42.2/24"}, target.AttachmentIPs["uplink"])
}

func TestEdgeResourceHandlersRegistered(t *testing.T) {
	for _, typ := range []string{"sysbox_firewall", "sysbox_ssh_access"} {
		p, ok := GetResourceHandler(typ)
		require.True(t, ok, typ)
		require.Equal(t, typ, p.Type())
		require.Equal(t, typ, p.Schema().Type)
	}
	_, actorRegistered := GetResourceHandler("sysbox_actor")
	require.False(t, actorRegistered)
}

func TestFirewallResourceHandlerPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_firewall", "allow_ssh"),
		Data: &config.FirewallConfig{
			AttachTo: "sysbox_router.edge.id",
			Rules: []config.FirewallRule{{
				Name: "ssh", Direction: "forward", Protocol: "tcp",
				DestinationPorts: []string{"22"}, SourceCIDRs: []string{"10.0.0.0/24"}, Verdict: "accept",
			}},
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_firewall", "allow_ssh"), Driver: "network", Attributes: inst}
	p := FirewallResourceHandler{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.FirewallConfig{
		AttachTo: "sysbox_router.edge.id",
		Rules: []config.FirewallRule{{
			Name: "https", Direction: "forward", Protocol: "tcp",
			DestinationPorts: []string{"443"}, SourceCIDRs: []string{"10.0.0.0/24"}, Verdict: "accept",
		}},
	}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	_, ok := fieldChangeAt(action.Changes, "rules[0].DestinationPorts[0]")
	require.True(t, ok)
}

func TestSSHAccessResourceHandlerPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_ssh_access", "admin"),
		Data: &config.SSHAccessConfig{
			Node:           "sysbox_node.web.id",
			AuthorizedKeys: []string{"ssh-ed25519 old"},
			Port:           22,
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_ssh_access", "admin"), Driver: "docker", Attributes: inst}
	p := SSHAccessResourceHandler{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.SSHAccessConfig{
		Node:           "sysbox_node.web.id",
		AuthorizedKeys: []string{"ssh-ed25519 new"},
		Port:           22,
	}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	change, ok := fieldChangeAt(action.Changes, "authorized_keys[0]")
	require.True(t, ok)
	require.True(t, change.Sensitive)
}

func TestEdgeProviderDeleteRemovesState(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    ResourceHandler
		res  state.Resource
	}{
		{
			name: "ssh_access",
			p:    SSHAccessResourceHandler{},
			res:  state.Resource{Address: address.Resource("sysbox_ssh_access", "admin")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{tc.res}}
			exec := NewExecutor(graph.New(), st)
			require.NoError(t, tc.p.Delete(context.Background(), &ProviderContext{exec: exec}, tc.res))
			require.Nil(t, st.FindResource(tc.res.Address))
		})
	}
}

// Observe reads the ruleset back (via nsenter in production) and compares the
// observed digest against the desired digest recorded at apply time.
func TestFirewallReadDetectsDigestDrift(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })

	match := &firewallPolicyFake{digest: "verified"}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-match", Version: "1", Policy: match}))
	res := state.Resource{
		Address: address.Resource("sysbox_firewall", "edge"),
		Driver:  "policy-match",
		Attributes: state.MustAttributes(map[string]any{
			"owner": "lab/sysbox_firewall.edge", "attach_to": "sysbox_router.edge",
			"policy_target_state": policyTargetFixture(t), "desired_digest": "verified",
		}),
	}
	result, err := (FirewallResourceHandler{}).Read(context.Background(), res)
	require.NoError(t, err)
	require.Equal(t, state.ResourcePresent, result.Status)

	tampered := &firewallPolicyFake{digest: "tampered"}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-tampered", Version: "1", Policy: tampered}))
	res.Driver = "policy-tampered"
	result, err = (FirewallResourceHandler{}).Read(context.Background(), res)
	require.NoError(t, err)
	require.Equal(t, state.ResourceDrifted, result.Status)
}

func TestFirewallIdentityConflictIsUnknown(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	fake := &firewallPolicyFake{observeErr: driver.Wrap(driver.ErrorInvalidState, "policy-conflict", "owner mismatch", nil)}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "policy-conflict", Version: "test", Policy: fake}))
	res := state.Resource{Address: address.Resource("sysbox_firewall", "edge"), Driver: "policy-conflict", Attributes: state.MustAttributes(map[string]any{"owner": "owner", "attach_to": "sysbox_router.edge", "policy_target_state": policyTargetFixture(t), "desired_digest": "expected"})}
	observed, err := (FirewallResourceHandler{}).Read(context.Background(), res)
	require.Error(t, err)
	require.Equal(t, state.ResourceUnknown, observed.Status)
}
