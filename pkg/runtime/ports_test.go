package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestNormalizePortSpecsDefaultsToDirectTCP(t *testing.T) {
	ports, err := normalizePortSpecs([]config.PortConfig{
		{Name: "web", Target: 8080},
	})

	require.NoError(t, err)
	require.Equal(t, []substrate.PortSpec{
		{Name: "web", Target: 8080, Protocol: "tcp", Exposure: substrate.PortExposureDirect},
	}, ports)
}

func TestNormalizePortSpecsRejectsHostExposureWithoutPublishedPort(t *testing.T) {
	_, err := normalizePortSpecs([]config.PortConfig{
		{Name: "web", Target: 8080, Exposure: substrate.PortExposureHost},
	})

	require.ErrorContains(t, err, "published is required for host exposure")
}

func TestNormalizePortSpecsRejectsDuplicateHostBinding(t *testing.T) {
	_, err := normalizePortSpecs([]config.PortConfig{
		{Name: "web", Target: 80, Published: 8080, Protocol: "http", Exposure: substrate.PortExposureHost},
		{Name: "api", Target: 8080, Published: 8080, Protocol: "tcp", Exposure: substrate.PortExposureHost},
	})

	require.ErrorContains(t, err, "duplicate host binding")
}

func TestResolvePortsBuildsDirectAndHostURLs(t *testing.T) {
	ports := resolvePorts([]substrate.PortSpec{
		{Name: "web", Target: 80, Published: 8080, Protocol: "http", Exposure: substrate.PortExposureHost},
		{Name: "ssh", Target: 22, Protocol: "tcp", Exposure: substrate.PortExposureDirect},
	}, "10.0.0.10")

	require.Len(t, ports, 2)
	require.Equal(t, "http://127.0.0.1:8080", ports[0].URL)
	require.Equal(t, "tcp://10.0.0.10:22", ports[1].URL)
	require.Equal(t, "10.0.0.10", ports[1].TargetHost)
}
