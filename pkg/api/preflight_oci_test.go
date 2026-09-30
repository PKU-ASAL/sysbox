package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

type ociPreflightArtifact struct {
	check     substrate.PreflightCheck
	resolves  int
	inspected []string
}

func (d *ociPreflightArtifact) ResolveImage(context.Context, substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	d.resolves++
	return substrate.ArtifactHandle{}, fmt.Errorf("preflight must not resolve images")
}

func (d *ociPreflightArtifact) PreflightImage(_ context.Context, source substrate.ArtifactSource) []substrate.PreflightCheck {
	d.inspected = append(d.inspected, source.Source)
	return []substrate.PreflightCheck{d.check}
}

func TestPreflightEndpointOCI(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check substrate.PreflightCheck
		ok    bool
	}{
		{"missing", substrate.PreflightCheck{OK: true, Severity: "warning", Message: "example:latest not cached; apply will pull", Hint: "docker load or use a pullable reference"}, true},
		{"mismatch", substrate.PreflightCheck{OK: false, Severity: "error", Message: "example:latest digest mismatch"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &ociPreflightArtifact{check: tc.check}
			previous := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			t.Cleanup(func() { driver.DefaultRegistry = previous })
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Artifact: d}))
			dir := t.TempDir()
			workspaces := filepath.Join(dir, "workspaces")
			topology := filepath.Join(workspaces, "lab")
			require.NoError(t, os.MkdirAll(topology, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(topology, "field.sysbox.hcl"), []byte(`
substrate "docker" { alias = "local" }
resource "sysbox_image" "lab" {
    substrate = substrate.docker.local
    kind = "oci"
    source = "example:latest"
    architecture = "amd64"
    guest_family = "linux"
}
`), 0644))
			s := NewServer(filepath.Join(dir, "runs"), workspaces)
			request := httptest.NewRequest(http.MethodGet, "/v1/topologies/lab/preflight", nil)
			request.SetPathValue("topology", "lab")
			response := httptest.NewRecorder()
			s.handlePreflight(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var result preflightResult
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Len(t, result.Checks, 1)
			require.Equal(t, tc.ok, result.OK)
			require.Equal(t, "image:lab:oci", result.Checks[0].Name)
			require.Equal(t, tc.check.Severity, result.Checks[0].Severity)
			require.Equal(t, tc.check.Message, result.Checks[0].Message)
			require.Equal(t, tc.check.Hint, result.Checks[0].Hint)
			require.Zero(t, d.resolves)
			require.Equal(t, []string{"example:latest"}, d.inspected)
		})
	}
}
