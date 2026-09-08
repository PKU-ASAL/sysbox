package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
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

func TestDestroyNodeTreatsMissingContainerAsDestroyed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/v1.47/containers/missing-container", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"message":"No such container: missing-container"}`, http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cli.Close()) })
	sub := &Substrate{cli: cli}

	err = sub.DestroyNode(context.Background(), substrate.NodeHandle{ID: "missing-container"})
	require.NoError(t, err)
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

func TestLaunchConfigUsesImageEntryForNonShellEntrypoint(t *testing.T) {
	cfg, direct := launchConfig([]string{"/nodejs/bin/node"}, []string{"/juice-shop/build/app.js"}, &Config{})
	require.True(t, direct)
	require.EqualValues(t, []string{"/nodejs/bin/node"}, cfg.Entrypoint)
	require.EqualValues(t, []string{"/juice-shop/build/app.js"}, cfg.Cmd)
}

func TestLaunchConfigKeepsShellForProvisioningWithoutEntrypoint(t *testing.T) {
	cfg, direct := launchConfig(nil, []string{"sleep", "10"}, &Config{})
	require.False(t, direct)
	require.EqualValues(t, []string{"/bin/sh", "-c"}, cfg.Entrypoint)
	require.EqualValues(t, []string{"sleep infinity"}, cfg.Cmd)
}

func TestCreateNodeAppliesCPUAndMemoryLimits(t *testing.T) {
	var got container.HostConfig
	var captured bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/containers/"):
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/containers/create"):
			var body struct {
				HostConfig container.HostConfig `json:"HostConfig"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			got = body.HostConfig
			captured = true
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: "container-id"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cli.Close()) })
	sub := &Substrate{cli: cli}

	_, err = sub.CreateNode(context.Background(), substrate.NodeSpec{
		Name:           "limited",
		Image:          substrate.ArtifactHandle{ID: "alpine:3.22"},
		VCPUs:          1,
		Memory:         "1024",
		ProviderConfig: &Config{CpusetCpus: "0"},
	})
	require.NoError(t, err)
	require.True(t, captured)
	require.Equal(t, int64(1e9), got.NanoCPUs)
	require.Equal(t, int64(1024*1024*1024), got.Memory)
	require.Equal(t, "0", got.CpusetCpus)
}

func TestCreateNodeDefaultsLeaveLimitsUnset(t *testing.T) {
	var got container.HostConfig
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/containers/"):
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/containers/create"):
			var body struct {
				HostConfig container.HostConfig `json:"HostConfig"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			got = body.HostConfig
			_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: "container-id"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cli.Close()) })
	sub := &Substrate{cli: cli}

	_, err = sub.CreateNode(context.Background(), substrate.NodeSpec{
		Name:  "defaults",
		Image: substrate.ArtifactHandle{ID: "alpine:3.22"},
	})
	require.NoError(t, err)
	require.Zero(t, got.NanoCPUs)
	require.Zero(t, got.Memory)
	require.Empty(t, got.CpusetCpus)
}

func TestParseMemoryMiB(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"1024MiB", 1024},
		{"2GiB", 2048},
		{"1GB", 1024},
	} {
		got, err := parseMemoryMiB(c.in)
		require.NoError(t, err)
		require.Equal(t, c.want, got)
	}
}
