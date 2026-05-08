package network

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// FirewallRuleSpec describes a single nftables rule to install.
type FirewallRuleSpec struct {
	Proto  string // "tcp" | "udp" | "all"
	DPort  int    // 0 = any
	SrcNet string // "" = any; CIDR like "10.0.2.0/24" for source-subnet filtering
	Action string // "accept" | "drop"
}

// ApplyFirewall installs a FORWARD chain with the given rules in the named
// netns using nftables. Table "sysbox_fw" is created (or flushed) fresh.
func ApplyFirewall(nsName string, rules []FirewallRuleSpec) error {
	return inNetns(nsName, func() error {
		conn, err := nftables.New()
		if err != nil {
			return fmt.Errorf("nftables.New: %w", err)
		}

		tbl := &nftables.Table{Family: nftables.TableFamilyIPv4, Name: "sysbox_fw"}
		conn.DelTable(tbl)
		tbl = conn.AddTable(tbl)

		chain := conn.AddChain(&nftables.Chain{
			Name:     "forward",
			Table:    tbl,
			Type:     nftables.ChainTypeFilter,
			Hooknum:  nftables.ChainHookForward,
			Priority: nftables.ChainPriorityFilter,
		})

		for _, r := range rules {
			exprs, err := buildExprs(r)
			if err != nil {
				return fmt.Errorf("build rule %+v: %w", r, err)
			}
			conn.AddRule(&nftables.Rule{Table: tbl, Chain: chain, Exprs: exprs})
		}

		return conn.Flush()
	})
}

// DeleteFirewall removes the sysbox_fw table from the netns.
func DeleteFirewall(nsName string) error {
	return inNetns(nsName, func() error {
		conn, err := nftables.New()
		if err != nil {
			return nil
		}
		conn.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: "sysbox_fw"})
		return conn.Flush()
	})
}

func buildExprs(r FirewallRuleSpec) ([]expr.Any, error) {
	var exprs []expr.Any

	// Source network match (IPv4 src addr + mask).
	if r.SrcNet != "" {
		srcExprs, err := srcNetExprs(r.SrcNet)
		if err != nil {
			return nil, fmt.Errorf("src_net %q: %w", r.SrcNet, err)
		}
		exprs = append(exprs, srcExprs...)
	}

	// Proto match (skip for "all").
	if r.Proto == "tcp" || r.Proto == "udp" {
		proto := uint8(unix.IPPROTO_TCP)
		if r.Proto == "udp" {
			proto = unix.IPPROTO_UDP
		}
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		)
	} else if r.Proto != "all" {
		return nil, fmt.Errorf("unsupported proto %q (use tcp|udp|all)", r.Proto)
	}

	// Destination port match.
	if r.DPort > 0 {
		if r.Proto == "all" {
			return nil, fmt.Errorf("dport requires proto tcp or udp, not all")
		}
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, uint16(r.DPort))
		exprs = append(exprs,
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseTransportHeader,
				Offset:       2,
				Len:          2,
			},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: b},
		)
	}

	// Verdict.
	switch r.Action {
	case "accept":
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictAccept})
	case "drop":
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictDrop})
	default:
		return nil, fmt.Errorf("unsupported action %q (use accept|drop)", r.Action)
	}

	return exprs, nil
}

// srcNetExprs returns nftables expressions that match IPv4 source address
// against the given CIDR. Uses bitwise AND with the mask then compare to
// the masked network address.
func srcNetExprs(cidr string) ([]expr.Any, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ip = ip.To4()
	if ip == nil {
		return nil, fmt.Errorf("only IPv4 CIDRs supported, got %s", cidr)
	}

	// IPv4 src addr is at offset 12, len 4, in the network header.
	// We AND with the mask and compare to the network address.
	maskBytes := []byte(ipnet.Mask)
	netBytes := []byte(ipnet.IP.To4())

	return []expr.Any{
		&expr.Payload{
			DestRegister: 1,
			Base:         expr.PayloadBaseNetworkHeader,
			Offset:       12,
			Len:          4,
		},
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           maskBytes,
			Xor:            []byte{0, 0, 0, 0},
		},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: netBytes},
	}, nil
}
