package config

import (
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/stretchr/testify/require"
)

func TestResolveResourceAddress(t *testing.T) {
	tests := []struct {
		ref  string
		typ  string
		want address.Address
	}{
		{"web", "sysbox_node", address.Resource("sysbox_node", "web")},
		{`web[0]`, "sysbox_node", address.IntInstance("sysbox_node", "web", 0)},
		{`web["blue"]`, "sysbox_node", address.StringInstance("sysbox_node", "web", "blue")},
		{"sysbox_node.web", "sysbox_node", address.Resource("sysbox_node", "web")},
		{"module.lab.sysbox_node.web", "sysbox_node", address.Resource("sysbox_node", "web").WithModule(address.ModuleInstance{Name: "lab"})},
	}
	for _, tt := range tests {
		got, err := ResolveResourceAddress(tt.ref, tt.typ)
		require.NoError(t, err, tt.ref)
		require.True(t, tt.want.Equal(got), tt.ref)
	}
}

func TestResolveResourceAddressRejectsWrongType(t *testing.T) {
	_, err := ResolveResourceAddress("sysbox_network.dmz", "sysbox_node")
	require.ErrorContains(t, err, "expected sysbox_node")
}

func TestResolveName(t *testing.T) {
	// Bare names pass through.
	require.Equal(t, "alpine", ResolveName("alpine"))
	require.Equal(t, "docker", ResolveName("docker"))

	// Empty string returns empty.
	require.Equal(t, "", ResolveName(""))
}

func TestLooksLikeKernelRef(t *testing.T) {
	// Reference-style names return true.
	require.True(t, LooksLikeKernelRef("sysbox_kernel.fc_510.id"))
	require.True(t, LooksLikeKernelRef("fc_510"))

	// Empty string returns false.
	require.False(t, LooksLikeKernelRef(""))
}
