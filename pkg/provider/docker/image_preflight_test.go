package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

type imagePreflightTransport func(*http.Request) (*http.Response, error)

func (f imagePreflightTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOCIImagePreflight(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name, expected, severity, message string
		resolved                          string
		status                            int
		transportErr                      error
		timeout                           bool
		ok                                bool
	}{
		{name: "matching", expected: digest, status: 200, severity: "info", ok: true, message: digest},
		{name: "normalized", expected: strings.Repeat("A", 64), status: 200, severity: "info", ok: true, message: digest},
		{name: "resolved reference", resolved: "registry.example/lab:latest", expected: digest, status: 200, severity: "info", ok: true, message: digest},
		{name: "mismatch", expected: other, status: 200, severity: "error", message: other},
		{name: "unpinned", status: 200, severity: "info", ok: true, message: "not pinned"},
		{name: "missing", expected: digest, status: 404, severity: "warning", ok: true, message: "not cached"},
		{name: "permission", status: 403, severity: "error", message: "inspect"},
		{name: "daemon error", status: 500, severity: "error", message: "inspect"},
		{name: "connection", transportErr: errors.New("connection refused"), severity: "error", message: "inspect"},
		{name: "timeout", timeout: true, severity: "error", message: "inspect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			transport := imagePreflightTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, http.MethodGet, r.Method, "preflight must never pull or load")
				reference := "local-image-test:latest"
				if tc.resolved != "" {
					reference = tc.resolved
				}
				require.Equal(t, "/v1.47/images/"+reference+"/json", r.URL.Path)
				if tc.timeout {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				body := fmt.Sprintf(`{"Id":%q,"RepoDigests":[%q]}`, digest, "example@"+other)
				if tc.status != 200 {
					body = `{"message":"daemon rejected inspect"}`
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			cli, err := client.NewClientWithOpts(client.WithHost("http://docker.invalid"), client.WithVersion("1.47"), client.WithHTTPClient(&http.Client{Transport: transport}))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, cli.Close()) })
			checker, ok := any(&Substrate{cli: cli}).(driver.ArtifactPreflight)
			require.True(t, ok, "Docker must expose read-only artifact preflight")
			timeout := time.Second
			if tc.timeout {
				timeout = 10 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			checks := checker.PreflightImage(ctx, substrate.ArtifactSource{Kind: substrate.ArtifactOCI, Source: "local-image-test:latest", ResolvedSource: tc.resolved, ExpectedDigest: tc.expected})
			require.Len(t, checks, 1)
			require.Equal(t, tc.ok, checks[0].OK)
			require.Equal(t, tc.severity, checks[0].Severity)
			require.Contains(t, checks[0].Message, tc.message)
			require.Contains(t, checks[0].Message, "local-image-test:latest")
			require.Equal(t, 1, calls)
			if tc.name == "mismatch" {
				require.Contains(t, checks[0].Message, digest)
			}
			if tc.status == 404 {
				require.Contains(t, checks[0].Message, "apply")
				require.Contains(t, checks[0].Message, "pull")
				require.Contains(t, checks[0].Hint, "docker load")
				require.Contains(t, checks[0].Hint, "pullable")
				require.Contains(t, checks[0].Hint, "daemon")
			}
		})
	}
}

func TestOCIImagePreflightRejectsNonOCIWithoutIO(t *testing.T) {
	// No client is configured: unsupported kinds must return before daemon I/O.
	checks := (&Substrate{}).PreflightImage(context.Background(), substrate.ArtifactSource{Kind: substrate.ArtifactRootFS, Source: "disk.ext4"})
	require.Len(t, checks, 1)
	require.False(t, checks[0].OK)
	require.Equal(t, "error", checks[0].Severity)
	require.Contains(t, checks[0].Message, "requires artifact kind")
}
