package network

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/google/nftables/userdata"
	"github.com/vishvananda/netns"
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
	err = inNetns(state.Namespace, func() error {
		return applyCompiledRuleset(plan)
	})
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "network", "apply ruleset", err)
	}
	return driver.RulesetObservation{Table: plan.Table, Digest: plan.Digest}, nil
}

// CheckTarget reports whether the network namespace the ruleset was applied to
// still exists. Like the docker driver, it does not read the ruleset back.
func (Driver) CheckTarget(_ context.Context, target driver.PolicyTarget) (bool, error) {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return false, err
	}
	ns, err := netns.GetFromName(state.Namespace)
	if err != nil {
		return false, nil
	}
	_ = ns.Close()
	return true, nil
}

func (Driver) DeleteRuleset(_ context.Context, target driver.PolicyTarget, owner string) error {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return err
	}
	return inNetns(state.Namespace, func() error {
		conn, err := nftables.New()
		if err != nil {
			return err
		}
		conn.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: driver.RulesetTableName(owner)})
		// Best-effort: an already-absent table is the common case, and any other
		// removal failure is self-healed by the next apply, which always rebuilds
		// the table from scratch.
		_ = conn.Flush()
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
	return applyCompiledRulesetConn(conn, plan)
}

func applyCompiledRulesetConn(conn *nftables.Conn, plan compiledRuleset) error {
	table := &nftables.Table{Family: nftables.TableFamilyIPv4, Name: plan.Table}
	// Always rebuild: delete any pre-existing table first, ignoring the result.
	// Deleting an absent table is ENOENT (the common first-apply case), and a
	// real error (e.g. permission) surfaces on the AddTable/Flush below.
	conn.DelTable(table)
	_ = conn.Flush()
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
	conn.AddRule(&nftables.Rule{Table: table, Chain: chains[driver.DirectionInput], UserData: userdata.AppendString(nil, userdata.TypeComment, expressionMarker(marker, nil))})
	// Loopback is always accepted so a node's access to its own 127.0.0.1
	// services survives a default drop policy — otherwise a `curl 127.0.0.1`
	// health check on a firewalled node is silently dropped.
	conn.AddRule(&nftables.Rule{Table: table, Chain: chains[driver.DirectionInput], Exprs: loopbackAcceptExpressions(expr.MetaKeyIIFNAME)})
	conn.AddRule(&nftables.Rule{Table: table, Chain: chains[driver.DirectionOutput], Exprs: loopbackAcceptExpressions(expr.MetaKeyOIFNAME)})
	for _, rule := range plan.Rules {
		expressions, err := policyExpressions(rule)
		if err != nil {
			return err
		}
		comment := expressionMarker(marker, expressions)
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
		conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: expressions, UserData: userdata.AppendString(nil, userdata.TypeComment, expressionMarker(marker+";nat=masquerade", expressions))})
	}
	return conn.Flush()
}

func ApplyRulesetInNetNSFD(fd int, spec driver.RulesetSpec, bindings map[string]string) (driver.RulesetObservation, error) {
	plan, err := compileRuleset(spec, bindings)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	conn, err := nftables.New(nftables.WithNetNSFd(fd))
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	if err := applyCompiledRulesetConn(conn, plan); err != nil {
		return driver.RulesetObservation{}, err
	}
	return driver.RulesetObservation{Table: plan.Table, Digest: plan.Digest}, nil
}

func DeleteRulesetInNetNSFD(fd int, owner string) error {
	conn, err := nftables.New(nftables.WithNetNSFd(fd))
	if err != nil {
		return err
	}
	conn.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: driver.RulesetTableName(owner)})
	// Best-effort: an already-absent table is the common case, and any other
	// removal failure self-heals on the next apply, which always rebuilds.
	_ = conn.Flush()
	return nil
}

func ownershipMarker(owner, digest string) string {
	return ownershipPrefix + owner + ";digest=" + digest
}

func expressionMarker(marker string, expressions []expr.Any) string {
	return marker + ";expr=" + expressionSignature(expressions)
}

func expressionSignature(expressions []expr.Any) string {
	canonical := make([]json.RawMessage, 0, len(expressions))
	for _, expression := range expressions {
		var payload []byte
		if _, ok := expression.(*expr.Counter); ok {
			payload = []byte(`"counter"`)
		} else {
			payload, _ = json.Marshal(struct {
				Type string `json:"type"`
				Data any    `json:"data"`
			}{fmt.Sprintf("%T", expression), expression})
		}
		canonical = append(canonical, payload)
	}
	payload, _ := json.Marshal(canonical)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8])
}

func markerValue(comment, key string) (string, bool) {
	prefix := key + "="
	for _, part := range strings.Split(comment, ";") {
		if strings.HasPrefix(part, prefix) {
			return strings.TrimPrefix(part, prefix), true
		}
	}
	return "", false
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

// loopbackAcceptExpressions matches traffic on the loopback interface (iifname
// or oifname == "lo") and accepts it. It is emitted for both the input and
// output chains so a node's own 127.0.0.1 traffic is never dropped by a default
// drop policy.
func loopbackAcceptExpressions(metaKey expr.MetaKey) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: metaKey, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifnameBytes("lo")},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

func ctStateMask(state driver.ConnectionState) uint32 {
	return map[driver.ConnectionState]uint32{driver.StateInvalid: 1, driver.StateEstablished: 2, driver.StateRelated: 4, driver.StateNew: 8}[state]
}
