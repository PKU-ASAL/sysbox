package network

import (
	"testing"

	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
)

func TestCompileRulesetBuildsOwnedBaseChainsAndBindings(t *testing.T) {
	spec := driver.RulesetSpec{
		Owner: "topology.lab/sysbox_firewall.edge", Family: driver.FamilyIPv4,
		DefaultInput: driver.VerdictDrop, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop,
		Rules: []driver.PolicyRule{{
			ID: "https", Direction: driver.DirectionForward,
			SourceCIDRs: []string{"10.0.0.7/24"}, DestinationCIDRs: []string{"192.0.2.10/32"},
			Protocol: driver.ProtocolTCP, DestinationPorts: []driver.PortRange{{From: 443, To: 443}},
			InputAttachment: "inside", OutputAttachment: "uplink",
			States: []driver.ConnectionState{driver.StateNew}, Verdict: driver.VerdictAccept, Counter: true,
		}},
	}

	plan, err := compileRuleset(spec, map[string]string{"inside": "eth1", "uplink": "eth0"})
	require.NoError(t, err)
	require.Equal(t, driver.RulesetTableName(spec.Owner), plan.Table)
	require.Equal(t, spec.Owner, plan.Owner)
	require.Equal(t, map[string]driver.Verdict{"input": driver.VerdictDrop, "output": driver.VerdictAccept, "forward": driver.VerdictDrop}, plan.BaseChains)
	require.Len(t, plan.Rules, 1)
	require.Equal(t, "eth1", plan.Rules[0].InputDevice)
	require.Equal(t, "eth0", plan.Rules[0].OutputDevice)
	require.Equal(t, []string{"10.0.0.0/24"}, plan.Rules[0].Rule.SourceCIDRs)
	require.NotEmpty(t, plan.Digest)
}

func TestPolicyExpressionsIncludePortsCounterAndVerdict(t *testing.T) {
	expressions, err := policyExpressions(compiledRule{Rule: driver.PolicyRule{
		Direction: driver.DirectionForward, Protocol: driver.ProtocolTCP,
		SourcePorts: []driver.PortRange{{From: 1024, To: 65535}}, DestinationPorts: []driver.PortRange{{From: 443, To: 443}},
		Verdict: driver.VerdictAccept, Counter: true,
	}})
	require.NoError(t, err)
	var payloads, ranges, counters, verdicts int
	for _, expression := range expressions {
		switch expression.(type) {
		case *expr.Payload:
			payloads++
		case *expr.Range:
			ranges++
		case *expr.Counter:
			counters++
		case *expr.Verdict:
			verdicts++
		}
	}
	require.GreaterOrEqual(t, payloads, 2)
	require.Equal(t, 1, ranges)
	require.Equal(t, 1, counters)
	require.Equal(t, 1, verdicts)
}

func TestCompileRulesetBuildsMasquerade(t *testing.T) {
	spec := driver.RulesetSpec{Owner: "topology.lab/sysbox_router.edge", Family: driver.FamilyIPv4,
		NAT: &driver.NATPolicy{SourceAttachment: "inside", UplinkAttachment: "uplink", SourceCIDRs: []string{"10.0.0.0/24"}, Masquerade: true}}
	plan, err := compileRuleset(spec, map[string]string{"inside": "eth1", "uplink": "eth0"})
	require.NoError(t, err)
	require.NotNil(t, plan.NAT)
	require.Equal(t, "eth1", plan.NAT.SourceDevice)
	require.Equal(t, "eth0", plan.NAT.UplinkDevice)
	require.True(t, plan.NAT.Policy.Masquerade)
}

func TestCompileRulesetRejectsUnknownLogicalAttachment(t *testing.T) {
	_, err := compileRuleset(driver.RulesetSpec{Owner: "owner", Family: driver.FamilyIPv4, Rules: []driver.PolicyRule{{
		Direction: driver.DirectionForward, Protocol: driver.ProtocolAll, InputAttachment: "missing", Verdict: driver.VerdictAccept,
	}}}, map[string]string{})
	require.ErrorContains(t, err, `logical attachment "missing"`)
}

func TestOwnershipMarkerRoundTripUsesFullOwner(t *testing.T) {
	owner := "topology.research/module.red/sysbox_firewall.edge"
	comment := ownershipMarker(owner, "abc123") + ";rule=https"
	gotOwner, digest, ok := parseOwnershipMarker(comment)
	require.True(t, ok)
	require.Equal(t, owner, gotOwner)
	require.Equal(t, "abc123", digest)
}
