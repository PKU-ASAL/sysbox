package network

import (
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
)

// nftScript renders a compiledRuleset as an `nft -f` script. The script rebuilds
// the table from scratch: one base chain per direction, a loopback-accept pair,
// then each policy rule, then (optionally) a NAT postrouting masquerade chain.
// Every owned rule carries the ownership marker comment so the readback parser
// can recover the owner and digest from `nft list ruleset` output.
func nftScript(plan compiledRuleset) string {
	var b strings.Builder
	table := plan.Table
	marker := ownershipMarker(plan.Owner, plan.Digest)

	fmt.Fprintf(&b, "add table ip %s\n", table)
	for _, c := range []struct {
		name    string
		hook    string
		verdict driver.Verdict
	}{
		{"input", "input", plan.BaseChains["input"]},
		{"output", "output", plan.BaseChains["output"]},
		{"forward", "forward", plan.BaseChains["forward"]},
	} {
		fmt.Fprintf(&b, "add chain ip %s %s { type filter hook %s priority 0; policy %s; }\n",
			table, c.name, c.hook, chainPolicyVerdict(c.verdict))
	}

	// The loopback accepts always carry the ownership marker, guaranteeing at
	// least one marker in the table even when the plan has no rules or NAT. nft
	// only accepts `comment` as a trailing statement, so it must follow the
	// verdict rather than stand alone.
	fmt.Fprintf(&b, "add rule ip %s input iifname %q accept comment %q\n", table, "lo", marker)
	fmt.Fprintf(&b, "add rule ip %s output oifname %q accept comment %q\n", table, "lo", marker)

	for _, rule := range plan.Rules {
		fmt.Fprintf(&b, "add rule ip %s %s %s%s comment %q\n",
			table, rule.Rule.Direction, ruleStatements(rule), verdictToken(rule.Rule.Verdict), marker)
	}

	if plan.NAT != nil && plan.NAT.Policy.Masquerade {
		fmt.Fprintf(&b, "add chain ip %s postrouting { type nat hook postrouting priority 100; }\n", table)
		var nat strings.Builder
		fmt.Fprintf(&nat, "oifname %q", plan.NAT.UplinkDevice)
		for _, cidr := range plan.NAT.Policy.SourceCIDRs {
			fmt.Fprintf(&nat, " ip saddr %s", cidr)
		}
		nat.WriteString(" masquerade")
		fmt.Fprintf(&b, "add rule ip %s postrouting %s comment %q\n", table, nat.String(), marker+";nat=masquerade")
	}

	return b.String()
}

// ruleStatements renders the match/action statements for one compiled rule in
// the same order the nftables library applied them: interface, address,
// protocol, ports, connection state, counter, log, then the verdict.
func ruleStatements(rule compiledRule) string {
	var b strings.Builder
	if rule.InputDevice != "" {
		fmt.Fprintf(&b, "iifname %q ", rule.InputDevice)
	}
	if rule.OutputDevice != "" {
		fmt.Fprintf(&b, "oifname %q ", rule.OutputDevice)
	}
	for _, cidr := range rule.Rule.SourceCIDRs {
		fmt.Fprintf(&b, "ip saddr %s ", cidr)
	}
	for _, cidr := range rule.Rule.DestinationCIDRs {
		fmt.Fprintf(&b, "ip daddr %s ", cidr)
	}
	if rule.Rule.Protocol != driver.ProtocolAll {
		fmt.Fprintf(&b, "meta l4proto %s ", protoToken(rule.Rule.Protocol))
	}
	if len(rule.Rule.SourcePorts) > 0 {
		fmt.Fprintf(&b, "%s sport %s ", protoToken(rule.Rule.Protocol), portRangeToken(rule.Rule.SourcePorts[0]))
	}
	if len(rule.Rule.DestinationPorts) > 0 {
		fmt.Fprintf(&b, "%s dport %s ", protoToken(rule.Rule.Protocol), portRangeToken(rule.Rule.DestinationPorts[0]))
	}
	if len(rule.Rule.States) > 0 {
		fmt.Fprintf(&b, "ct state %s ", stateTokens(rule.Rule.States))
	}
	if rule.Rule.Counter {
		b.WriteString("counter ")
	}
	if rule.Rule.Log {
		prefix := "sysbox"
		if rule.Rule.ID != "" {
			prefix += ":" + rule.Rule.ID
		}
		fmt.Fprintf(&b, "limit rate 10/second burst 20 packets log prefix %q ", prefix)
	}
	return b.String()
}

func chainPolicyVerdict(v driver.Verdict) string {
	if v == driver.VerdictAccept {
		return "accept"
	}
	// nft base-chain policy only supports accept or drop; reject collapses to drop.
	return "drop"
}

func verdictToken(v driver.Verdict) string {
	switch v {
	case driver.VerdictAccept:
		return "accept"
	case driver.VerdictReject:
		return "reject"
	default:
		return "drop"
	}
}

func protoToken(p driver.Protocol) string { return string(p) }

func stateTokens(states []driver.ConnectionState) string {
	parts := make([]string, len(states))
	for i, s := range states {
		parts[i] = string(s)
	}
	return strings.Join(parts, ",")
}

func portRangeToken(p driver.PortRange) string {
	if p.From == p.To {
		return fmt.Sprintf("%d", p.From)
	}
	return fmt.Sprintf("%d-%d", p.From, p.To)
}
