package docker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestBackgroundExecStatusRejectsImmediateFailure(t *testing.T) {
	_, complete, err := backgroundExecStatus(container.ExecInspect{Running: false, ExitCode: 127})
	require.True(t, complete)
	require.ErrorContains(t, err, "code 127")

	pid, complete, err := backgroundExecStatus(container.ExecInspect{Running: true, Pid: 42})
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, 42, pid)
}

func TestDockerProviderStateRoundTripsEffectiveLaunch(t *testing.T) {
	sub := &Substrate{}
	want := &HandleState{
		ContainerName:   "service",
		ImageEntrypoint: []string{"/entry"},
		ImageCmd:        []string{"serve", "--debug"},
	}
	raw, err := sub.MarshalProviderState(substrate.NodeHandle{ID: "container-id", Provider: want})
	require.NoError(t, err)
	require.True(t, json.Valid(raw))

	restored, err := sub.UnmarshalProviderState(raw)
	require.NoError(t, err)
	require.Equal(t, want, restored)
}

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

func TestNormalizeBindsResolvesRelativeHostSources(t *testing.T) {
	binds, err := normalizeBinds([]string{"fixtures/keycloak/import:/opt/keycloak/data/import:ro", "/var/lib/data:/data:ro"})
	if err != nil {
		t.Fatalf("normalize binds: %v", err)
	}
	if !strings.HasPrefix(binds[0], "/") || !strings.HasSuffix(binds[0], ":/opt/keycloak/data/import:ro") {
		t.Fatalf("relative bind was not resolved: %q", binds[0])
	}
	if binds[1] != "/var/lib/data:/data:ro" {
		t.Fatalf("absolute bind changed: %q", binds[1])
	}
}

func TestValidateHostPortExposureIsValidatedByRuntimeAttachments(t *testing.T) {
	sub := &Substrate{}
	err := sub.Validate(substrate.NodeSpec{
		Ports: []substrate.PortSpec{
			{Name: "http", Target: 80, Published: 28080, Exposure: substrate.PortExposureHost},
		},
	})
	require.NoError(t, err)
}
