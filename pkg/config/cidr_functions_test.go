package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestCidrsubnet(t *testing.T) {
	cases := []struct {
		prefix  string
		newbits int64
		netnum  int64
		want    string
	}{
		{"10.200.0.0/16", 8, 0, "10.200.0.0/24"},
		{"10.200.0.0/16", 8, 1, "10.200.1.0/24"},
		{"10.200.0.0/16", 8, 99, "10.200.99.0/24"},
		{"10.1.2.3/16", 8, 0, "10.1.0.0/24"},
	}
	for _, c := range cases {
		got, err := cidrsubnetFunc.Call([]cty.Value{
			cty.StringVal(c.prefix), cty.NumberIntVal(c.newbits), cty.NumberIntVal(c.netnum),
		})
		require.NoError(t, err, c.prefix)
		require.Equal(t, c.want, got.AsString(), c.prefix)
	}
}

func TestCidrsubnetErrors(t *testing.T) {
	for _, c := range [][]cty.Value{
		{cty.StringVal("not-a-cidr"), cty.NumberIntVal(8), cty.NumberIntVal(0)},
		{cty.StringVal("2001:db8::/32"), cty.NumberIntVal(8), cty.NumberIntVal(0)},
		{cty.StringVal("10.0.0.0/16"), cty.NumberFloatVal(8.9), cty.NumberIntVal(0)},
		{cty.StringVal("10.0.0.0/16"), cty.NumberIntVal(-1), cty.NumberIntVal(0)},
		{cty.StringVal("10.0.0.0/16"), cty.NumberIntVal(17), cty.NumberIntVal(0)},
		{cty.StringVal("10.0.0.0/16"), cty.NumberIntVal(8), cty.NumberIntVal(256)},
	} {
		_, err := cidrsubnetFunc.Call(c)
		require.Error(t, err)
	}
}

func TestCidrhost(t *testing.T) {
	cases := []struct {
		prefix  string
		hostnum int64
		want    string
	}{
		{"10.200.0.0/24", 0, "10.200.0.0"},
		{"10.200.0.0/24", 1, "10.200.0.1"},
		{"10.200.0.0/24", 10, "10.200.0.10"},
		{"10.200.0.99/24", 5, "10.200.0.5"},
	}
	for _, c := range cases {
		got, err := cidrhostFunc.Call([]cty.Value{
			cty.StringVal(c.prefix), cty.NumberIntVal(c.hostnum),
		})
		require.NoError(t, err, c.prefix)
		require.Equal(t, c.want, got.AsString(), c.prefix)
	}
}

func TestCidrhostErrors(t *testing.T) {
	for _, c := range [][]cty.Value{
		{cty.StringVal("bad"), cty.NumberIntVal(0)},
		{cty.StringVal("10.0.0.0/24"), cty.NumberIntVal(-1)},
		{cty.StringVal("10.0.0.0/24"), cty.NumberIntVal(256)},
	} {
		_, err := cidrhostFunc.Call(c)
		require.Error(t, err)
	}
}

func TestCidrsubnetInHCL(t *testing.T) {
	root, err := ParseString(`
resource "sysbox_network" "lab" {
  cidr = cidrsubnet("10.200.0.0/16", 8, 99)
}
`, "test.hcl")
	require.NoError(t, err)
	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	var cfg NetworkConfig
	require.NoError(t, DecodeResource(&root.Resources[0], &cfg, ctx))
	require.Equal(t, "10.200.99.0/24", cfg.CIDR)
}
