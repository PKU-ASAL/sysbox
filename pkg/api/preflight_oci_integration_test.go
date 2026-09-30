package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
)

// TestPreflightOCIRealDocker uses an existing cached image and a real local
// daemon. All daemon traffic passes through a read-only allowlist; the test
// never creates, pulls, loads, tags, runs, or removes images/containers.
// Opt in with SYSBOX_TEST_DOCKER_IMAGE=<cached reference or image ID>.
func TestPreflightOCIRealDocker(t *testing.T) {
	cached := os.Getenv("SYSBOX_TEST_DOCKER_IMAGE")
	if cached == "" {
		t.Skip("SYSBOX_TEST_DOCKER_IMAGE must name an existing cached image for real Docker integration")
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	require.True(t, strings.HasPrefix(host, "unix://"), "this integration test requires a local Unix-socket daemon")
	socket := strings.TrimPrefix(host, "unix://")

	// No request outside this list can reach the real daemon. Recording denied
	// attempts also makes accidental ResolveImage/pull reuse fail the test.
	allowed := regexp.MustCompile(`^(/v[0-9]+[.][0-9]+)?/(_ping|version|images/.+/json)$`)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "docker"})
	proxy.Transport = transport
	var mu sync.Mutex
	var denied []string
	inspections := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readOnly := r.Method == http.MethodGet || (r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/_ping"))
		mu.Lock()
		if !readOnly || !allowed.MatchString(r.URL.Path) {
			denied = append(denied, r.Method+" "+r.URL.RequestURI())
			mu.Unlock()
			http.Error(w, "only daemon version/ping and image inspect are allowed", http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/json") {
			inspections++
		}
		mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(gateway.Close)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		require.Empty(t, denied, "unexpected daemon requests (blocked before forwarding)")
		t.Logf("real Docker image-inspect requests=%d; forbidden requests=%d", inspections, len(denied))
	})
	// Docker CLI requires tcp://; the Go SDK accepts it as well.
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(gateway.URL, "http://"))
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cli.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	img, err := cli.ImageInspect(ctx, cached)
	require.NoError(t, err, "explicitly enabled integration must fail, not skip, if the fixture/daemon is unavailable")
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, img.ID)

	provider, err := dockerprovider.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Close()) })
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "integration", Node: provider, Artifact: provider}))
	cfg := config.DefaultServiceConfig()
	dir := t.TempDir()
	cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir = filepath.Join(dir, "runs"), filepath.Join(dir, "workspaces")
	cfg.State.Backend = ""
	server := NewServerWithConfig(cfg)
	require.NoError(t, server.initErr)
	api := httptest.NewServer(server)
	t.Cleanup(api.Close)
	httpClient := api.Client()
	httpClient.Timeout = 90 * time.Second

	preflight := func(t *testing.T, topology, hcl string) preflightResult {
		t.Helper()
		topologyDir := filepath.Join(cfg.Paths.WorkspacesDir, topology)
		require.NoError(t, os.MkdirAll(topologyDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(topologyDir, "field.sysbox.hcl"), []byte(hcl), 0600))
		response, err := httpClient.Get(api.URL + "/v1/topologies/" + topology + "/preflight")
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var result preflightResult
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		return result
	}
	wrong := "sha256:" + strings.Repeat("0", 64)
	if wrong == img.ID {
		wrong = "sha256:" + strings.Repeat("1", 64)
	}
	imageHCL := func(name, source, digest string) string {
		return fmt.Sprintf(`
resource "sysbox_image" %q {
    substrate = substrate.docker.local
    kind = "oci"
    source = %q
    sha256 = %q
    architecture = "amd64"
    guest_family = "linux"
}`, name, source, digest)
	}
	const substrateHCL = `substrate "docker" { alias = "local" }`
	for _, tc := range []struct {
		name, source, digest, severity string
		ok                             bool
	}{
		{"cached-match", cached, img.ID, "info", true},
		{"cached-mismatch", cached, wrong, "error", false},
		{"cached-unpinned", cached, "", "info", true},
		{"missing-local", "sysbox-preflight-missing-" + uuid.NewString() + ":latest", img.ID, "warning", true},
		{"missing-registry", "registry.invalid/sysbox-preflight-" + uuid.NewString() + ":latest", "", "warning", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hcl := substrateHCL + imageHCL("lab", tc.source, tc.digest)
			result := preflight(t, tc.name, hcl)
			require.Equal(t, tc.ok, result.OK, "%+v", result.Checks)
			require.Len(t, result.Checks, 2, "daemon and resource checks must both run")
			for _, check := range result.Checks {
				if check.Name == "docker_daemon" {
					require.True(t, check.OK)
					continue
				}
				require.Equal(t, "image:lab:oci", check.Name)
				require.Equal(t, tc.severity, check.Severity)
				require.Equal(t, tc.ok, check.OK)
				require.Contains(t, check.Message, tc.source)
				if tc.severity == "warning" {
					require.Contains(t, check.Message, "pull")
					require.Contains(t, check.Hint, "docker load")
					require.Contains(t, check.Hint, "pullable")
				}
			}
			t.Logf("ok=%t image_severity=%s", result.OK, tc.severity)
		})
	}

	// Multi-resource aggregation must be tested with self-contained fixtures,
	// without depending on a consumer repository or its naming conventions.
	t.Run("multiple-images", func(t *testing.T) {
		images := []struct{ name, source, digest, severity string }{
			{"cached", cached, img.ID, "info"},
			{"missing_local", "sysbox-preflight-missing-" + uuid.NewString() + ":latest", img.ID, "warning"},
			{"missing_registry", "registry.invalid/sysbox-preflight-" + uuid.NewString() + ":latest", "", "warning"},
		}
		hcl := substrateHCL
		for _, image := range images {
			hcl += imageHCL(image.name, image.source, image.digest)
		}
		result := preflight(t, "multiple-images", hcl)
		require.True(t, result.OK, "%+v", result.Checks)
		require.Len(t, result.Checks, len(images)+1)
		byName := map[string]preflightCheck{}
		for _, check := range result.Checks {
			byName[check.Name] = check
		}
		require.True(t, byName["docker_daemon"].OK)
		for _, image := range images {
			check, found := byName["image:"+image.name+":oci"]
			require.True(t, found, image.name)
			require.True(t, check.OK)
			require.Equal(t, image.severity, check.Severity)
			require.Contains(t, check.Message, image.source)
			if image.severity == "warning" {
				require.Contains(t, check.Hint, "docker load")
				require.Contains(t, check.Hint, "pullable")
			}
		}
	})
}
