package config

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// cidrsubnetFunc implements cidrsubnet(prefix, newbits, netnum) → subnet CIDR
// string (IPv4 only), Terraform-compatible.
var cidrsubnetFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "prefix", Type: cty.String},
		{Name: "newbits", Type: cty.Number},
		{Name: "netnum", Type: cty.Number},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		prefix, err := parseIPv4Prefix(args[0].AsString())
		if err != nil {
			return cty.NilVal, err
		}
		newbits, _ := args[1].AsBigFloat().Int64()
		netnum, _ := args[2].AsBigFloat().Int64()
		if newbits < 0 {
			return cty.NilVal, fmt.Errorf("newbits must be non-negative")
		}
		newLen := prefix.Bits() + int(newbits)
		if newLen > 32 {
			return cty.NilVal, fmt.Errorf("prefix extension would exceed 32 bits")
		}
		if netnum < 0 || netnum >= (int64(1)<<uint(newbits)) {
			return cty.NilVal, fmt.Errorf("subnet number out of range")
		}
		return cty.StringVal(netip.PrefixFrom(subnetAddr(prefix, int(newbits), int(netnum)), newLen).String()), nil
	},
})

// cidrhostFunc implements cidrhost(prefix, hostnum) → host IP string (IPv4
// only), Terraform-compatible.
var cidrhostFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "prefix", Type: cty.String},
		{Name: "hostnum", Type: cty.Number},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		prefix, err := parseIPv4Prefix(args[0].AsString())
		if err != nil {
			return cty.NilVal, err
		}
		hostnum, _ := args[1].AsBigFloat().Int64()
		if hostnum < 0 || hostnum >= (int64(1)<<uint(32-prefix.Bits())) {
			return cty.NilVal, fmt.Errorf("host number out of range")
		}
		return cty.StringVal(hostAddr(prefix, int(hostnum)).String()), nil
	},
})

// parseIPv4Prefix parses an IPv4 prefix and normalizes it to its network
// address (Masked). IPv6 is rejected.
func parseIPv4Prefix(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR prefix")
	}
	if !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("IPv6 not supported")
	}
	return prefix.Masked(), nil
}

func subnetAddr(prefix netip.Prefix, newbits, netnum int) netip.Addr {
	subnetSize := uint32(1) << uint(32-prefix.Bits()-newbits)
	return fromU32(baseU32(prefix) + uint32(netnum)*subnetSize)
}

func hostAddr(prefix netip.Prefix, hostnum int) netip.Addr {
	return fromU32(baseU32(prefix) + uint32(hostnum))
}

func baseU32(prefix netip.Prefix) uint32 {
	b := prefix.Addr().As4()
	return binary.BigEndian.Uint32(b[:])
}

func fromU32(v uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}
