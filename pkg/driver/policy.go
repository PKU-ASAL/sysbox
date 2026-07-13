package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

type AddressFamily string
type Verdict string
type Direction string
type Protocol string
type ConnectionState string

const (
	FamilyIPv4 AddressFamily = "ipv4"

	VerdictAccept Verdict = "accept"
	VerdictDrop   Verdict = "drop"
	VerdictReject Verdict = "reject"

	DirectionInput   Direction = "input"
	DirectionOutput  Direction = "output"
	DirectionForward Direction = "forward"

	ProtocolAll  Protocol = "all"
	ProtocolTCP  Protocol = "tcp"
	ProtocolUDP  Protocol = "udp"
	ProtocolICMP Protocol = "icmp"

	StateNew         ConnectionState = "new"
	StateEstablished ConnectionState = "established"
	StateRelated     ConnectionState = "related"
	StateInvalid     ConnectionState = "invalid"
)

type PortRange struct{ From, To uint16 }

type PolicyRule struct {
	ID               string
	Direction        Direction
	SourceCIDRs      []string
	DestinationCIDRs []string
	SourcePorts      []PortRange
	DestinationPorts []PortRange
	Protocol         Protocol
	InputAttachment  string
	OutputAttachment string
	States           []ConnectionState
	Verdict          Verdict
	Counter          bool
	Log              bool
}

type NATPolicy struct {
	SourceAttachment string
	UplinkAttachment string
	SourceCIDRs      []string
	Masquerade       bool
}

type RulesetSpec struct {
	Owner          string
	Family         AddressFamily
	DefaultInput   Verdict
	DefaultOutput  Verdict
	DefaultForward Verdict
	Rules          []PolicyRule
	NAT            *NATPolicy
}

type PolicyTarget struct {
	Resource string
	State    json.RawMessage
}

type OwnedObject struct{ Kind, Name string }

type RulesetObservation struct {
	Table     string
	Digest    string
	Inventory []OwnedObject
}

type Policy interface {
	ApplyRuleset(context.Context, PolicyTarget, RulesetSpec) (RulesetObservation, error)
	ObserveRuleset(context.Context, PolicyTarget, string) (RulesetObservation, error)
	DeleteRuleset(context.Context, PolicyTarget, string) error
}

func NormalizeRuleset(spec RulesetSpec) (RulesetSpec, error) {
	if spec.Owner == "" {
		return spec, fmt.Errorf("policy owner is required")
	}
	if len(spec.Owner) > 128 {
		return spec, fmt.Errorf("policy owner exceeds 128 bytes required for nftables ownership markers")
	}
	if spec.Family == "" {
		spec.Family = FamilyIPv4
	}
	if spec.Family != FamilyIPv4 {
		return spec, fmt.Errorf("address family %q is not supported; only IPv4 is supported", spec.Family)
	}
	for _, policy := range []*Verdict{&spec.DefaultInput, &spec.DefaultOutput, &spec.DefaultForward} {
		if *policy == "" {
			*policy = VerdictDrop
		}
		if !validVerdict(*policy) {
			return spec, fmt.Errorf("invalid default verdict %q", *policy)
		}
	}
	for i := range spec.Rules {
		r := &spec.Rules[i]
		if r.Protocol == "" {
			r.Protocol = ProtocolAll
		}
		if r.Direction != DirectionInput && r.Direction != DirectionOutput && r.Direction != DirectionForward {
			return spec, fmt.Errorf("rule %d: invalid direction %q", i, r.Direction)
		}
		if r.Protocol != ProtocolAll && r.Protocol != ProtocolTCP && r.Protocol != ProtocolUDP && r.Protocol != ProtocolICMP {
			return spec, fmt.Errorf("rule %d: invalid protocol %q", i, r.Protocol)
		}
		if !validVerdict(r.Verdict) {
			return spec, fmt.Errorf("rule %d: invalid verdict %q", i, r.Verdict)
		}
		if (len(r.SourcePorts) > 0 || len(r.DestinationPorts) > 0) && r.Protocol != ProtocolTCP && r.Protocol != ProtocolUDP {
			return spec, fmt.Errorf("rule %d: ports require tcp or udp", i)
		}
		var err error
		if r.SourceCIDRs, err = normalizeCIDRs(r.SourceCIDRs); err != nil {
			return spec, fmt.Errorf("rule %d source: %w", i, err)
		}
		if r.DestinationCIDRs, err = normalizeCIDRs(r.DestinationCIDRs); err != nil {
			return spec, fmt.Errorf("rule %d destination: %w", i, err)
		}
		if err := validatePorts(r.SourcePorts); err != nil {
			return spec, fmt.Errorf("rule %d source ports: %w", i, err)
		}
		if err := validatePorts(r.DestinationPorts); err != nil {
			return spec, fmt.Errorf("rule %d destination ports: %w", i, err)
		}
		r.States, err = normalizeStates(r.States)
		if err != nil {
			return spec, fmt.Errorf("rule %d: %w", i, err)
		}
	}
	if spec.NAT != nil {
		if spec.NAT.SourceAttachment == "" || spec.NAT.UplinkAttachment == "" {
			return spec, fmt.Errorf("NAT source and uplink attachments are required")
		}
		var err error
		spec.NAT.SourceCIDRs, err = normalizeCIDRs(spec.NAT.SourceCIDRs)
		if err != nil {
			return spec, fmt.Errorf("NAT source: %w", err)
		}
	}
	return spec, nil
}

func RulesetDigest(spec RulesetSpec, bindings map[string]string) (string, error) {
	normalized, err := NormalizeRuleset(spec)
	if err != nil {
		return "", err
	}
	type binding struct{ Logical, Device string }
	ordered := make([]binding, 0, len(bindings))
	for logical, device := range bindings {
		ordered = append(ordered, binding{logical, device})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Logical < ordered[j].Logical })
	payload, err := json.Marshal(struct {
		Version  int
		Spec     RulesetSpec
		Bindings []binding
	}{1, normalized, ordered})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func RulesetTableName(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	base := strings.ToLower(regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(owner, "_"))
	base = strings.Trim(base, "_")
	if len(base) > 13 {
		base = base[len(base)-13:]
	}
	return fmt.Sprintf("sysbox_%s_%s", base, hex.EncodeToString(sum[:5]))
}

func validVerdict(v Verdict) bool {
	return v == VerdictAccept || v == VerdictDrop || v == VerdictReject
}

func normalizeCIDRs(input []string) ([]string, error) {
	out := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, raw := range input {
		ip, network, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q", raw)
		}
		if ip.To4() == nil {
			return nil, fmt.Errorf("IPv6 is not supported: %s", raw)
		}
		value := network.String()
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out, nil
}

func validatePorts(ports []PortRange) error {
	for _, p := range ports {
		if p.From == 0 || p.To == 0 || p.From > p.To {
			return fmt.Errorf("invalid range %d-%d", p.From, p.To)
		}
	}
	return nil
}

func normalizeStates(states []ConnectionState) ([]ConnectionState, error) {
	order := map[ConnectionState]int{StateNew: 0, StateEstablished: 1, StateRelated: 2, StateInvalid: 3}
	seen := map[ConnectionState]bool{}
	for _, state := range states {
		if _, ok := order[state]; !ok {
			return nil, fmt.Errorf("invalid connection state %q", state)
		}
		seen[state] = true
	}
	out := make([]ConnectionState, 0, len(seen))
	for state := range seen {
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i]] < order[out[j]] })
	return out, nil
}
