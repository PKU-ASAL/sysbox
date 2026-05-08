package network

import (
	"encoding/binary"
	"fmt"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// FirewallRuleSpec describes a single nftables rule to install.
type FirewallRuleSpec struct {
	Proto  string // "tcp" | "udp" | "all"
	DPort  int    // 0 = any
	SrcNet string // "" = any (Phase 1: SrcNet != "" is logged but ignored)
	Action string // "accept" | "drop"
}

// ApplyFirewall installs a FORWARD chain with the given rules in the named
// netns using nftables. Table "sysbox_fw" is created (or flushed) fresh.
//
// Runs inside the netns so nftables netlink connects to the right namespace.
func ApplyFirewall(nsName string, rules []FirewallRuleSpec) error {
	return inNetns(nsName, func() error {
		conn, err := nftables.New()
		if err != nil {
			return fmt.Errorf("nftables.New: %w", err)
		}

		// Drop and recreate the table for idempotency.
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
			if r.SrcNet != "" {
				fmt.Printf("[firewall] warning: src_net %q not implemented in Phase 1, skipping match\n", r.SrcNet)
			}
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
			return nil // best-effort
		}
		conn.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: "sysbox_fw"})
		return conn.Flush()
	})
}

func buildExprs(r FirewallRuleSpec) ([]expr.Any, error) {
	var exprs []expr.Any

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
