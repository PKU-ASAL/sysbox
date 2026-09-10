package network

import (
	"fmt"
	"testing"

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

func TestCompileRulesetExpandsMatchListsAsAlternatives(t *testing.T) {
	spec := driver.RulesetSpec{
		Owner: "topology.lab/sysbox_firewall.service", Family: driver.FamilyIPv4,
		Rules: []driver.PolicyRule{{
			ID: "service", Direction: driver.DirectionInput, Protocol: driver.ProtocolTCP,
			SourceCIDRs:      []string{"10.0.0.2/32", "127.0.0.0/8"},
			DestinationPorts: []driver.PortRange{{From: 8080, To: 8080}, {From: 8443, To: 8443}},
			Verdict:          driver.VerdictAccept,
		}},
	}

	plan, err := compileRuleset(spec, nil)
	require.NoError(t, err)
	require.Len(t, plan.Rules, 4)
	for _, rule := range plan.Rules {
		require.Len(t, rule.Rule.SourceCIDRs, 1)
		require.Len(t, rule.Rule.DestinationPorts, 1)
	}
}

func TestNFTScriptRendersRuleMatches(t *testing.T) {
	spec := driver.RulesetSpec{
		Owner: "topology.lab/sysbox_firewall.edge", Family: driver.FamilyIPv4,
		Rules: []driver.PolicyRule{{
			ID: "https", Direction: driver.DirectionForward,
			SourceCIDRs: []string{"10.0.0.0/24"}, DestinationCIDRs: []string{"192.0.2.0/24"},
			Protocol:    driver.ProtocolTCP,
			SourcePorts: []driver.PortRange{{From: 1024, To: 65535}}, DestinationPorts: []driver.PortRange{{From: 443, To: 443}},
			InputAttachment: "inside", OutputAttachment: "uplink",
			States: []driver.ConnectionState{driver.StateNew}, Verdict: driver.VerdictAccept, Counter: true,
		}},
	}
	plan, err := compileRuleset(spec, map[string]string{"inside": "eth1", "uplink": "eth0"})
	require.NoError(t, err)
	script := nftScript(plan)

	require.Contains(t, script, `iifname "eth1"`)
	require.Contains(t, script, `oifname "eth0"`)
	require.Contains(t, script, "ip saddr 10.0.0.0/24")
	require.Contains(t, script, "ip daddr 192.0.2.0/24")
	require.Contains(t, script, "meta l4proto tcp")
	require.Contains(t, script, "tcp sport 1024-65535")
	require.Contains(t, script, "tcp dport 443")
	require.Contains(t, script, "ct state new")
	require.Contains(t, script, "counter")
	require.Contains(t, script, "accept")
	require.Contains(t, script, fmt.Sprintf("accept comment %q", ownershipMarker(spec.Owner, plan.Digest)))
}

func TestNFTScriptRendersLogAndReject(t *testing.T) {
	spec := driver.RulesetSpec{
		Owner: "topology.lab/sysbox_firewall.edge", Family: driver.FamilyIPv4,
		Rules: []driver.PolicyRule{{
			ID: "block", Direction: driver.DirectionInput, Protocol: driver.ProtocolUDP,
			DestinationPorts: []driver.PortRange{{From: 53, To: 53}},
			Verdict:          driver.VerdictReject, Log: true,
		}},
	}
	plan, err := compileRuleset(spec, nil)
	require.NoError(t, err)
	script := nftScript(plan)
	require.Contains(t, script, "udp dport 53")
	require.Contains(t, script, `limit rate 10/second burst 20 packets log prefix "sysbox:block"`)
	require.Contains(t, script, "reject")
	require.Contains(t, script, fmt.Sprintf("reject comment %q", ownershipMarker(spec.Owner, plan.Digest)))
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

func TestOwnershipMarkerFormat(t *testing.T) {
	owner := "topology.research/module.red/sysbox_firewall.edge"
	require.Equal(t, "sysbox-owner=topology.research/module.red/sysbox_firewall.edge;digest=abc123", ownershipMarker(owner, "abc123"))
}

func TestNFTScriptRendersOwnedTable(t *testing.T) {
	spec := driver.RulesetSpec{
		Owner: "topology.lab/sysbox_firewall.edge", Family: driver.FamilyIPv4,
		DefaultInput: driver.VerdictDrop, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictReject,
	}
	plan, err := compileRuleset(spec, nil)
	require.NoError(t, err)
	script := nftScript(plan)

	require.Contains(t, script, "add table ip "+plan.Table)
	require.Contains(t, script, "add chain ip "+plan.Table+" input { type filter hook input priority 0; policy drop; }")
	require.Contains(t, script, "add chain ip "+plan.Table+" output { type filter hook output priority 0; policy accept; }")
	// reject collapses to drop for the base-chain policy.
	require.Contains(t, script, "add chain ip "+plan.Table+" forward { type filter hook forward priority 0; policy drop; }")
	marker := ownershipMarker(spec.Owner, plan.Digest)
	require.Contains(t, script, fmt.Sprintf(`iifname "lo" accept comment %q`, marker))
	require.Contains(t, script, fmt.Sprintf(`oifname "lo" accept comment %q`, marker))
	// `comment` is only valid as a trailing statement, never standalone.
	require.NotContains(t, script, " input comment ")
}

func TestNFTScriptRendersMasquerade(t *testing.T) {
	spec := driver.RulesetSpec{Owner: "topology.lab/sysbox_router.edge", Family: driver.FamilyIPv4,
		NAT: &driver.NATPolicy{SourceAttachment: "inside", UplinkAttachment: "uplink", SourceCIDRs: []string{"10.0.0.0/24"}, Masquerade: true}}
	plan, err := compileRuleset(spec, map[string]string{"inside": "eth1", "uplink": "eth0"})
	require.NoError(t, err)
	script := nftScript(plan)

	require.Contains(t, script, "add chain ip "+plan.Table+" postrouting { type nat hook postrouting priority 100; }")
	require.Contains(t, script, `oifname "eth0" ip saddr 10.0.0.0/24 masquerade`)
	require.Contains(t, script, fmt.Sprintf("masquerade comment %q", ownershipMarker(spec.Owner, plan.Digest)+";nat=masquerade"))
}

func TestObserveFromNFTListExtractsDigest(t *testing.T) {
	owner := "topology.lab/sysbox_firewall.edge"
	tableName := driver.RulesetTableName(owner)
	output := fmt.Sprintf(`table ip %s {
	chain input {
		type filter hook input priority filter; policy drop;
		iifname "lo" accept comment "sysbox-owner=%s;digest=abc123"
	}
	chain output {
		type filter hook output priority filter; policy accept;
		oifname "lo" accept comment "sysbox-owner=%s;digest=abc123"
	}
}
`, tableName, owner, owner)

	observation, err := observeFromNFTList(output, owner)
	require.NoError(t, err)
	require.Equal(t, tableName, observation.Table)
	require.Equal(t, "abc123", observation.Digest)
}

func TestObserveFromNFTListRejectsAbsentTable(t *testing.T) {
	owner := "topology.lab/sysbox_firewall.edge"
	_, err := observeFromNFTList("table ip other { }", owner)
	require.Error(t, err)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound))
}

func TestObserveFromNFTListRejectsForeignOwner(t *testing.T) {
	owner := "topology.lab/sysbox_firewall.edge"
	tableName := driver.RulesetTableName(owner)
	output := fmt.Sprintf(`table ip %s {
	chain input {
		comment "sysbox-owner=someone.else;digest=abc123"
	}
}
`, tableName)
	_, err := observeFromNFTList(output, owner)
	require.Error(t, err)
	require.True(t, driver.IsCategory(err, driver.ErrorInvalidState))
}

func TestObserveFromNFTListRejectsInconsistentDigests(t *testing.T) {
	owner := "topology.lab/sysbox_firewall.edge"
	tableName := driver.RulesetTableName(owner)
	output := fmt.Sprintf(`table ip %s {
	chain input {
		comment "sysbox-owner=%s;digest=abc123"
		comment "sysbox-owner=%s;digest=def456"
	}
}
`, tableName, owner, owner)
	_, err := observeFromNFTList(output, owner)
	require.Error(t, err)
	require.True(t, driver.IsCategory(err, driver.ErrorInvalidState))
}
