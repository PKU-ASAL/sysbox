package value

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathRoundTrip(t *testing.T) {
	for _, input := range []string{"cidr", "interfaces[0].ip", `labels["zone"]`, `rules[2].matches["dst"]`} {
		path, err := ParsePath(input)
		require.NoError(t, err, input)
		require.Equal(t, input, path.String())
	}
}

func TestDiffReportsStableNestedPaths(t *testing.T) {
	before, err := FromGo(map[string]any{"interfaces": []any{map[string]any{"ip": "10.0.0.1", "mtu": 1500}}})
	require.NoError(t, err)
	after, err := FromGo(map[string]any{"interfaces": []any{map[string]any{"ip": "10.0.0.2", "mtu": 1500}}, "enabled": true})
	require.NoError(t, err)

	changes := Diff(before, after)
	require.Equal(t, []Change{
		{Path: MustParsePath("enabled"), Before: Null(), After: Bool(true)},
		{Path: MustParsePath("interfaces[0].ip"), Before: String("10.0.0.1"), After: String("10.0.0.2")},
	}, changes)
}
