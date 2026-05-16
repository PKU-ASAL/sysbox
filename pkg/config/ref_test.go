package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveName(t *testing.T) {
	// Bare names pass through.
	require.Equal(t, "alpine", ResolveName("alpine"))
	require.Equal(t, "docker", ResolveName("docker"))

	// Dot-qualified references extract the middle component.
	require.Equal(t, "alpine", ResolveName("sysbox_image.alpine.id"))
	require.Equal(t, "dmz", ResolveName("sysbox_network.dmz.id"))
	require.Equal(t, "fc_510", ResolveName("sysbox_kernel.fc_510.id"))

	// Empty string returns empty.
	require.Equal(t, "", ResolveName(""))
}

func TestLooksLikeKernelRef(t *testing.T) {
	// Reference-style names return true.
	require.True(t, LooksLikeKernelRef("sysbox_kernel.fc_510.id"))
	require.True(t, LooksLikeKernelRef("fc_510"))

	// Literal paths return false.
	require.False(t, LooksLikeKernelRef("/path/to/vmlinux"))
	require.False(t, LooksLikeKernelRef("./vmlinux"))
	require.False(t, LooksLikeKernelRef("../kernels/vmlinux"))

	// URLs return false.
	require.False(t, LooksLikeKernelRef("https://example.com/vmlinux"))

	// Empty string returns false.
	require.False(t, LooksLikeKernelRef(""))
}
