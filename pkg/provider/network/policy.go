package network

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/driver"
)

type compiledRule struct {
	Rule         driver.PolicyRule
	InputDevice  string
	OutputDevice string
}

type compiledNAT struct {
	Policy       driver.NATPolicy
	SourceDevice string
	UplinkDevice string
}

type compiledRuleset struct {
	Owner      string
	Table      string
	Digest     string
	BaseChains map[string]driver.Verdict
	Rules      []compiledRule
	NAT        *compiledNAT
}

func compileRuleset(spec driver.RulesetSpec, bindings map[string]string) (compiledRuleset, error) {
	normalized, err := driver.NormalizeRuleset(spec)
	if err != nil {
		return compiledRuleset{}, err
	}
	plan := compiledRuleset{
		Owner: normalized.Owner,
		Table: driver.RulesetTableName(normalized.Owner),
		BaseChains: map[string]driver.Verdict{
			"input": normalized.DefaultInput, "output": normalized.DefaultOutput, "forward": normalized.DefaultForward,
		},
		Rules: make([]compiledRule, 0, len(normalized.Rules)),
	}
	for _, rule := range normalized.Rules {
		for _, alternative := range expandRuleAlternatives(rule) {
			compiled := compiledRule{Rule: alternative}
			if rule.InputAttachment != "" {
				compiled.InputDevice, err = requireBinding(bindings, rule.InputAttachment)
				if err != nil {
					return compiledRuleset{}, err
				}
			}
			if rule.OutputAttachment != "" {
				compiled.OutputDevice, err = requireBinding(bindings, rule.OutputAttachment)
				if err != nil {
					return compiledRuleset{}, err
				}
			}
			plan.Rules = append(plan.Rules, compiled)
		}
	}
	if normalized.NAT != nil {
		source, err := requireBinding(bindings, normalized.NAT.SourceAttachment)
		if err != nil {
			return compiledRuleset{}, err
		}
		uplink, err := requireBinding(bindings, normalized.NAT.UplinkAttachment)
		if err != nil {
			return compiledRuleset{}, err
		}
		plan.NAT = &compiledNAT{Policy: *normalized.NAT, SourceDevice: source, UplinkDevice: uplink}
	}
	plan.Digest, err = driver.RulesetDigest(normalized, bindings)
	if err != nil {
		return compiledRuleset{}, err
	}
	return plan, nil
}

func expandRuleAlternatives(rule driver.PolicyRule) []driver.PolicyRule {
	rules := []driver.PolicyRule{rule}
	rules = expandStringAlternatives(rules, rule.SourceCIDRs, func(rule *driver.PolicyRule, value string) { rule.SourceCIDRs = []string{value} })
	rules = expandStringAlternatives(rules, rule.DestinationCIDRs, func(rule *driver.PolicyRule, value string) { rule.DestinationCIDRs = []string{value} })
	rules = expandPortAlternatives(rules, rule.SourcePorts, func(rule *driver.PolicyRule, value driver.PortRange) { rule.SourcePorts = []driver.PortRange{value} })
	return expandPortAlternatives(rules, rule.DestinationPorts, func(rule *driver.PolicyRule, value driver.PortRange) { rule.DestinationPorts = []driver.PortRange{value} })
}

func expandStringAlternatives(rules []driver.PolicyRule, values []string, set func(*driver.PolicyRule, string)) []driver.PolicyRule {
	if len(values) < 2 {
		return rules
	}
	expanded := make([]driver.PolicyRule, 0, len(rules)*len(values))
	for _, rule := range rules {
		for _, value := range values {
			alternative := rule
			set(&alternative, value)
			expanded = append(expanded, alternative)
		}
	}
	return expanded
}

func expandPortAlternatives(rules []driver.PolicyRule, values []driver.PortRange, set func(*driver.PolicyRule, driver.PortRange)) []driver.PolicyRule {
	if len(values) < 2 {
		return rules
	}
	expanded := make([]driver.PolicyRule, 0, len(rules)*len(values))
	for _, rule := range rules {
		for _, value := range values {
			alternative := rule
			set(&alternative, value)
			expanded = append(expanded, alternative)
		}
	}
	return expanded
}

func requireBinding(bindings map[string]string, logical string) (string, error) {
	device := bindings[logical]
	if device == "" {
		return "", fmt.Errorf("logical attachment %q has no observed device binding", logical)
	}
	return device, nil
}
