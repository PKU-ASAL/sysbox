package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStateGetAddressSupportsCanonicalInstances(t *testing.T) {
	resource, attribute, err := parseStateGetAddress(`module.lab.sysbox_node.web["blue"].primary_ip`)
	require.NoError(t, err)
	require.Equal(t, `module.lab.sysbox_node.web["blue"]`, resource.String())
	require.Equal(t, "primary_ip", attribute)
}

func TestParseStateGetAddressPrefersCompleteResourceAddress(t *testing.T) {
	resource, attribute, err := parseStateGetAddress(`module.lab.sysbox_node.web[0]`)
	require.NoError(t, err)
	require.Equal(t, `module.lab.sysbox_node.web[0]`, resource.String())
	require.Empty(t, attribute)
}
