package address

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []string{
		`sysbox_node.web`,
		`sysbox_node.web[0]`,
		`sysbox_node.web["front-end"]`,
		`module.network.sysbox_network.dmz`,
		`module.segment["red"].sysbox_node.target[1]`,
	}
	for _, input := range cases {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(input)
			require.NoError(t, err)
			require.Equal(t, input, got.String())
		})
	}
}

func TestParseRejectsMalformedAddress(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		``,
		`sysbox_node`,
		`module_.node_x`,
		`sysbox_node.web[]`,
		`sysbox_node.web[key]`,
		`module.network`,
		`sysbox_node.web trailing`,
		`sysbox_node.web[-1]`,
	} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(input)
			require.Error(t, err)
			require.Contains(t, err.Error(), "byte")
		})
	}
}

func TestJSONUsesCanonicalString(t *testing.T) {
	t.Parallel()

	input := StringInstance("sysbox_node", "web", "front-end")
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	require.JSONEq(t, `"sysbox_node.web[\"front-end\"]"`, string(raw))

	var output Address
	require.NoError(t, json.Unmarshal(raw, &output))
	require.True(t, input.Equal(output))
}

func TestAddressesSortCanonically(t *testing.T) {
	t.Parallel()

	addresses := []Address{
		StringInstance("sysbox_node", "web", "blue"),
		Resource("sysbox_node", "web"),
		IntInstance("sysbox_node", "web", 1),
		IntInstance("sysbox_node", "web", 0),
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].Less(addresses[j]) })

	got := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		got = append(got, addr.String())
	}
	require.Equal(t, []string{
		`sysbox_node.web`,
		`sysbox_node.web[0]`,
		`sysbox_node.web[1]`,
		`sysbox_node.web["blue"]`,
	}, got)
}

func TestWithModuleCopiesModulePath(t *testing.T) {
	t.Parallel()

	base := Resource("sysbox_node", "web").WithModule(ModuleInstance{Name: "outer"})
	nested := base.WithModule(ModuleInstance{Name: "inner", Key: StringKeyValue("blue")})
	nested.ModulePath[0].Name = "mutated"

	require.Equal(t, `module.outer.sysbox_node.web`, base.String())
	require.Equal(t, `module.mutated.module.inner["blue"].sysbox_node.web`, nested.String())
}
