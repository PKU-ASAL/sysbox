package substrate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilityNames(t *testing.T) {
	cases := []struct {
		name          string
		substrateName string
		caps          Capabilities
		want          []string
	}{
		{
			name:          "docker contributes veth and the docker marker",
			substrateName: "docker",
			caps:          Capabilities{SharedKernel: true, NICKinds: []string{NICKindVeth}},
			want:          []string{"docker", "veth"},
		},
		{
			name:          "firecracker contributes tap",
			substrateName: "firecracker",
			caps:          Capabilities{NICKinds: []string{NICKindTap}},
			want:          []string{"firecracker", "tap"},
		},
		{
			name:          "libvirt contributes tap",
			substrateName: "libvirt",
			caps:          Capabilities{NICKinds: []string{"tap"}},
			want:          []string{"libvirt", "tap"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.ElementsMatch(t, tc.want, CapabilityNames(tc.substrateName, tc.caps))
		})
	}
}

func TestSupportedSubstrates(t *testing.T) {
	checks := map[string][]PreflightCheck{
		"docker": {
			{Name: "docker_socket", OK: true, Severity: "info"},
		},
		"firecracker": {
			{Name: "kvm_device", OK: false, Severity: "error"},
		},
		"libvirt": {
			// A warning-level failure downgrades but must not disqualify.
			{Name: "libvirt_isolation", OK: false, Severity: "warning"},
			{Name: "libvirt_socket", OK: true, Severity: "info"},
		},
	}

	supported, reasons := SupportedSubstrates(checks)

	require.ElementsMatch(t, []string{"docker", "libvirt"}, supported)
	require.Len(t, reasons, 1)
	require.Equal(t, "kvm_device", reasons["firecracker"])
}
