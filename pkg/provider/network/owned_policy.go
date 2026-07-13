package network

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/google/nftables/userdata"
	"golang.org/x/sys/unix"

	"github.com/oslab/sysbox/pkg/driver"
)

const ownershipPrefix = "sysbox-owner="

type policyTargetState struct {
	Namespace string            `json:"namespace"`
	Bindings  map[string]string `json:"bindings"`
}

func (Driver) ApplyRuleset(_ context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	plan, err := compileRuleset(spec, state.Bindings)
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "compile ruleset", err)
	}
	err = inNetns(state.Namespace, func() error { return applyCompiledRuleset(plan) })
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "network", "apply ruleset", err)
	}
	return Driver{}.ObserveRuleset(context.Background(), target, spec.Owner)
}

func (Driver) ObserveRuleset(_ context.Context, target driver.PolicyTarget, owner string) (driver.RulesetObservation, error) {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	var observation driver.RulesetObservation
	err = inNetns(state.Namespace, func() error {
		var observeErr error
		observation, observeErr = observeOwnedRuleset(owner)
		return observeErr
	})
	return observation, err
}

func (Driver) DeleteRuleset(_ context.Context, target driver.PolicyTarget, owner string) error {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return err
	}
	return inNetns(state.Namespace, func() error {
		observation, err := observeOwnedRuleset(owner)
		if driver.IsCategory(err, driver.ErrorNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		conn, err := nftables.New()
		if err != nil {
			return err
		}
		conn.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: observation.Table})
		if err := conn.Flush(); err != nil {
			return err
		}
		if _, err := observeOwnedRuleset(owner); !driver.IsCategory(err, driver.ErrorNotFound) {
			if err == nil {
				return fmt.Errorf("owned policy residue remains: table %s", observation.Table)
			}
			return fmt.Errorf("verify policy deletion: %w", err)
		}
		return nil
	})
}

func decodePolicyTarget(target driver.PolicyTarget) (policyTargetState, error) {
	var state policyTargetState
	if err := json.Unmarshal(target.State, &state); err != nil {
		return state, driver.Wrap(driver.ErrorInvalidState, "network", "decode policy target", err)
	}
	if state.Namespace == "" {
		return state, driver.Wrap(driver.ErrorInvalidState, "network", "policy namespace is required", nil)
	}
	if state.Bindings == nil {
		state.Bindings = map[string]string{}
	}
	return state, nil
}

func applyCompiledRuleset(plan compiledRuleset) error {
	conn, err := nftables.New()
	if err != nil {
		return err
	}
	table := &nftables.Table{Family: nftables.TableFamilyIPv4, Name: plan.Table}
	conn.DelTable(table)
	table = conn.AddTable(table)
	chains := map[driver.Direction]*nftables.Chain{}
	for _, item := range []struct {
		name      string
		direction driver.Direction
		hook      *nftables.ChainHook
	}{
		{"input", driver.DirectionInput, nftables.ChainHookInput},
		{"output", driver.DirectionOutput, nftables.ChainHookOutput},
		{"forward", driver.DirectionForward, nftables.ChainHookForward},
	} {
		policy, err := chainPolicy(plan.BaseChains[item.name])
		if err != nil {
			return err
		}
		chains[item.direction] = conn.AddChain(&nftables.Chain{Name: item.name, Table: table, Type: nftables.ChainTypeFilter, Hooknum: item.hook, Priority: nftables.ChainPriorityFilter, Policy: &policy})
	}
	marker := ownershipMarker(plan.Owner, plan.Digest)
	conn.AddRule(&nftables.Rule{Table: table, Chain: chains[driver.DirectionInput], UserData: userdata.AppendString(nil, userdata.TypeComment, marker)})
	for _, rule := range plan.Rules {
		expressions, err := policyExpressions(rule)
		if err != nil {
			return err
		}
		comment := marker
		if rule.Rule.ID != "" {
			comment += ";rule=" + rule.Rule.ID
		}
		conn.AddRule(&nftables.Rule{Table: table, Chain: chains[rule.Rule.Direction], Exprs: expressions, UserData: userdata.AppendString(nil, userdata.TypeComment, comment)})
	}
	if plan.NAT != nil && plan.NAT.Policy.Masquerade {
		chain := conn.AddChain(&nftables.Chain{Name: "postrouting", Table: table, Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
		expressions := []expr.Any{&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifnameBytes(plan.NAT.UplinkDevice)}}
		for _, cidr := range plan.NAT.Policy.SourceCIDRs {
			match, _ := cidrExpressions(cidr, true)
			expressions = append(expressions, match...)
		}
		expressions = append(expressions, &expr.Masq{})
		conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: expressions, UserData: userdata.AppendString(nil, userdata.TypeComment, marker+";nat=masquerade")})
	}
	return conn.Flush()
}

func observeOwnedRuleset(owner string) (driver.RulesetObservation, error) {
	conn, err := nftables.New()
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "network", "open nftables", err)
	}
	tableName := driver.RulesetTableName(owner)
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "network", "list nftables tables", err)
	}
	var table *nftables.Table
	for _, candidate := range tables {
		if candidate.Name == tableName {
			table = candidate
			break
		}
	}
	if table == nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorNotFound, "network", "owned ruleset not found", nil)
	}
	chains, err := conn.ListChains()
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	observation := driver.RulesetObservation{Table: tableName, Inventory: []driver.OwnedObject{{Kind: "table", Name: tableName}}}
	owned := false
	ruleCount := 0
	for _, chain := range chains {
		if chain.Table == nil || chain.Table.Name != tableName {
			continue
		}
		observation.Inventory = append(observation.Inventory, driver.OwnedObject{Kind: "chain", Name: chain.Name})
		rules, err := conn.GetRules(table, chain)
		if err != nil {
			return driver.RulesetObservation{}, err
		}
		for _, rule := range rules {
			ruleCount++
			comment, _ := userdata.GetString(rule.UserData, userdata.TypeComment)
			markerOwner, digest, ok := parseOwnershipMarker(comment)
			if !ok || markerOwner != owner {
				return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "owned table contains a rule without matching ownership marker", nil)
			}
			owned = true
			if observation.Digest != "" && observation.Digest != digest {
				return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "owned table contains inconsistent policy digests", nil)
			}
			observation.Digest = digest
			observation.Inventory = append(observation.Inventory, driver.OwnedObject{Kind: "rule", Name: chain.Name})
		}
	}
	if !owned || ruleCount == 0 {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "table name exists without matching ownership marker", nil)
	}
	return observation, nil
}

func ownershipMarker(owner, digest string) string {
	return ownershipPrefix + owner + ";digest=" + digest
}
func parseOwnershipMarker(comment string) (string, string, bool) {
	if !strings.HasPrefix(comment, ownershipPrefix) {
		return "", "", false
	}
	parts := strings.Split(comment, ";")
	owner := strings.TrimPrefix(parts[0], ownershipPrefix)
	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "digest=") {
			return owner, strings.TrimPrefix(part, "digest="), true
		}
	}
	return "", "", false
}

func chainPolicy(verdict driver.Verdict) (nftables.ChainPolicy, error) {
	switch verdict {
	case driver.VerdictAccept:
		return nftables.ChainPolicyAccept, nil
	case driver.VerdictDrop, driver.VerdictReject:
		return nftables.ChainPolicyDrop, nil
	default:
		return 0, fmt.Errorf("invalid chain policy %q", verdict)
	}
}

func policyExpressions(rule compiledRule) ([]expr.Any, error) {
	var out []expr.Any
	if rule.InputDevice != "" {
		out = append(out, &expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifnameBytes(rule.InputDevice)})
	}
	if rule.OutputDevice != "" {
		out = append(out, &expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifnameBytes(rule.OutputDevice)})
	}
	for _, cidr := range rule.Rule.SourceCIDRs {
		match, err := cidrExpressions(cidr, true)
		if err != nil {
			return nil, err
		}
		out = append(out, match...)
	}
	for _, cidr := range rule.Rule.DestinationCIDRs {
		match, err := cidrExpressions(cidr, false)
		if err != nil {
			return nil, err
		}
		out = append(out, match...)
	}
	if rule.Rule.Protocol != driver.ProtocolAll {
		proto := map[driver.Protocol]byte{driver.ProtocolTCP: unix.IPPROTO_TCP, driver.ProtocolUDP: unix.IPPROTO_UDP, driver.ProtocolICMP: unix.IPPROTO_ICMP}[rule.Rule.Protocol]
		out = append(out, &expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}})
	}
	var err error
	out, err = appendPortExpressions(out, rule.Rule.SourcePorts, 0)
	if err != nil {
		return nil, err
	}
	out, err = appendPortExpressions(out, rule.Rule.DestinationPorts, 2)
	if err != nil {
		return nil, err
	}
	if len(rule.Rule.States) > 0 {
		var mask uint32
		for _, state := range rule.Rule.States {
			mask |= ctStateMask(state)
		}
		data := make([]byte, 4)
		binary.LittleEndian.PutUint32(data, mask)
		out = append(out, &expr.Ct{Key: expr.CtKeySTATE, Register: 1}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: data, Xor: make([]byte, 4)}, &expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)})
	}
	if rule.Rule.Counter {
		out = append(out, &expr.Counter{})
	}
	if rule.Rule.Log {
		prefix := "sysbox"
		if rule.Rule.ID != "" {
			prefix += ":" + rule.Rule.ID
		}
		out = append(out,
			&expr.Limit{Type: expr.LimitTypePkts, Rate: 10, Unit: expr.LimitTimeSecond, Burst: 20},
			&expr.Log{Key: 1 << unix.NFTA_LOG_PREFIX, Data: append([]byte(prefix), 0)},
		)
	}
	switch rule.Rule.Verdict {
	case driver.VerdictAccept:
		out = append(out, &expr.Verdict{Kind: expr.VerdictAccept})
	case driver.VerdictDrop:
		out = append(out, &expr.Verdict{Kind: expr.VerdictDrop})
	case driver.VerdictReject:
		out = append(out, &expr.Reject{Type: unix.NFT_REJECT_ICMP_UNREACH, Code: 3})
	}
	return out, nil
}

func appendPortExpressions(out []expr.Any, ports []driver.PortRange, offset uint32) ([]expr.Any, error) {
	for _, port := range ports {
		from := make([]byte, 2)
		to := make([]byte, 2)
		binary.BigEndian.PutUint16(from, port.From)
		binary.BigEndian.PutUint16(to, port.To)
		out = append(out, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: offset, Len: 2})
		if port.From == port.To {
			out = append(out, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: from})
		} else {
			out = append(out, &expr.Range{Op: expr.CmpOpEq, Register: 1, FromData: from, ToData: to})
		}
	}
	return out, nil
}

func cidrExpressions(cidr string, source bool) ([]expr.Any, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	offset := uint32(16)
	if source {
		offset = 12
	}
	return []expr.Any{&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte(network.Mask), Xor: make([]byte, 4)}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(network.IP.To4())}}, nil
}
func ifnameBytes(name string) []byte { return append([]byte(name), 0) }
func ctStateMask(state driver.ConnectionState) uint32 {
	return map[driver.ConnectionState]uint32{driver.StateInvalid: 1, driver.StateEstablished: 2, driver.StateRelated: 4, driver.StateNew: 8}[state]
}
