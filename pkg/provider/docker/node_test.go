package docker

import (
	"testing"

	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestDockerPortConfigHostExposure(t *testing.T) {
	exposed, bindings, err := dockerPortConfig([]substrate.PortSpec{
		{Name: "http", Target: 80, Published: 28080, Protocol: "http", Exposure: substrate.PortExposureHost, HostIP: "127.0.0.1"},
		{Name: "dns", Target: 53, Published: 5300, Protocol: "udp", Exposure: substrate.PortExposureHost},
		{Name: "internal", Target: 8080, Protocol: "tcp", Exposure: substrate.PortExposureDirect},
	})

	require.NoError(t, err)
	httpPort := nat.Port("80/tcp")
	dnsPort := nat.Port("53/udp")
	require.Contains(t, exposed, httpPort)
	require.Contains(t, exposed, dnsPort)
	require.Equal(t, []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "28080"}}, bindings[httpPort])
	require.Equal(t, []nat.PortBinding{{HostPort: "5300"}}, bindings[dnsPort])
	require.NotContains(t, exposed, nat.Port("8080/tcp"))
}

func TestDockerPortConfigRequiresPublishedForHostExposure(t *testing.T) {
	_, _, err := dockerPortConfig([]substrate.PortSpec{
		{Name: "http", Target: 80, Protocol: "tcp", Exposure: substrate.PortExposureHost},
	})

	require.ErrorContains(t, err, "published must be positive")
}

func TestValidateHostPortExposureRequiresDockerNATLink(t *testing.T) {
	sub := &Substrate{}
	err := sub.Validate(substrate.NodeSpec{
		Ports: []substrate.PortSpec{
			{Name: "http", Target: 80, Published: 28080, Exposure: substrate.PortExposureHost},
		},
	})
	require.ErrorContains(t, err, "requires at least one nat=true sysbox_network link")

	err = sub.Validate(substrate.NodeSpec{
		Ports: []substrate.PortSpec{
			{Name: "http", Target: 80, Published: 28080, Exposure: substrate.PortExposureHost},
		},
		InitialLinks: []substrate.LinkRequest{
			{KindHint: substrate.NICKindDockerNAT, DockerNetID: "net-1"},
		},
	})
	require.NoError(t, err)
}
