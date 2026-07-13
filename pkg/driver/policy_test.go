package driver

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeRulesetRejectsIPv6(t *testing.T) {
	_, err := NormalizeRuleset(RulesetSpec{
		Owner:  "topology.lab/sysbox_firewall.edge",
		Family: FamilyIPv4,
		Rules:  []PolicyRule{{Direction: DirectionForward, SourceCIDRs: []string{"2001:db8::/64"}, Protocol: ProtocolAll, Verdict: VerdictAccept}},
	})
	require.ErrorContains(t, err, "IPv6 is not supported")
}

func TestNormalizeRulesetCanonicalizesCIDRsAndStates(t *testing.T) {
	got, err := NormalizeRuleset(RulesetSpec{
		Owner:         "topology.lab/sysbox_firewall.edge",
		Family:        FamilyIPv4,
		DefaultInput:  VerdictDrop,
		DefaultOutput: VerdictAccept,
		Rules: []PolicyRule{{
			Direction:        DirectionForward,
			SourceCIDRs:      []string{"10.0.0.7/24"},
			Protocol:         ProtocolTCP,
			DestinationPorts: []PortRange{{From: 443, To: 443}},
			States:           []ConnectionState{StateRelated, StateEstablished, StateRelated},
			Verdict:          VerdictAccept,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, VerdictDrop, got.DefaultForward)
	require.Equal(t, []string{"10.0.0.0/24"}, got.Rules[0].SourceCIDRs)
	require.Equal(t, []ConnectionState{StateEstablished, StateRelated}, got.Rules[0].States)
}

func TestNormalizeRulesetRejectsPortsWithAllProtocol(t *testing.T) {
	_, err := NormalizeRuleset(RulesetSpec{Owner: "owner", Family: FamilyIPv4, Rules: []PolicyRule{{
		Direction: DirectionForward, Protocol: ProtocolAll,
		DestinationPorts: []PortRange{{From: 53, To: 53}}, Verdict: VerdictAccept,
	}}})
	require.ErrorContains(t, err, "ports require tcp or udp")
}

func TestRulesetTableNameIsStableAndSafe(t *testing.T) {
	a := RulesetTableName("topology.lab/module.red/sysbox_firewall.edge")
	b := RulesetTableName("topology.lab/module.red/sysbox_firewall.edge")
	require.Equal(t, a, b)
	require.True(t, strings.HasPrefix(a, "sysbox_"))
	require.LessOrEqual(t, len(a), 31)
	require.Regexp(t, `^[a-z0-9_]+$`, a)
}

func TestRulesetDigestIsStableAcrossBindingOrder(t *testing.T) {
	spec := RulesetSpec{Owner: "owner", Family: FamilyIPv4, Rules: []PolicyRule{{
		Direction: DirectionForward, InputAttachment: "inside", OutputAttachment: "uplink",
		Protocol: ProtocolTCP, Verdict: VerdictAccept, Counter: true,
	}}}
	a, err := RulesetDigest(spec, map[string]string{"inside": "eth1", "uplink": "eth0"})
	require.NoError(t, err)
	b, err := RulesetDigest(spec, map[string]string{"uplink": "eth0", "inside": "eth1"})
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Len(t, a, 64)
}
