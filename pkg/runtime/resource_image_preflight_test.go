package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/substrate"
)

type preflightImageDriver struct {
	imageArtifactDriver
	source   substrate.ArtifactSource
	checks   []substrate.PreflightCheck
	called   bool
	deadline time.Time
}

func (d *preflightImageDriver) PreflightImage(ctx context.Context, source substrate.ArtifactSource) []substrate.PreflightCheck {
	d.called = true
	d.source = source
	d.deadline, _ = ctx.Deadline()
	return d.checks
}

func imagePreflightHCL(t *testing.T, source, provider string) []substrate.PreflightCheck {
	t.Helper()
	root, err := config.ParseString(fmt.Sprintf(`
resource "sysbox_image" "lab" {
    substrate = %q
    kind = "oci"
    source = %q
    sha256 = %q
    architecture = "amd64"
    guest_family = "linux"
}
`, provider, source, "sha256:"+strings.Repeat("a", 64)), "preflight.hcl")
	require.NoError(t, err)
	return ResourcePreflightChecks(root.Resources[0], nil)
}

func TestImagePreflightOCIUsesConfiguredDriver(t *testing.T) {
	d := &preflightImageDriver{checks: []substrate.PreflightCheck{{Name: "provider-image", OK: true, Severity: "warning", Message: "not cached", Hint: "docker load or pullable reference"}}}
	registerImageArtifactDriver(t, d)
	start := time.Now()
	checks := imagePreflightHCL(t, "cvelab-runtime-test:latest", "image-test")
	require.Len(t, checks, 1)
	require.Equal(t, "image:lab:oci", checks[0].Name)
	require.Equal(t, "warning", checks[0].Severity)
	require.Equal(t, d.checks[0].Message, checks[0].Message)
	require.Equal(t, d.checks[0].Hint, checks[0].Hint)
	require.True(t, d.called)
	require.Equal(t, substrate.ArtifactOCI, d.source.Kind)
	require.Equal(t, "cvelab-runtime-test:latest", d.source.Source)
	require.Equal(t, "sha256:"+strings.Repeat("a", 64), d.source.ExpectedDigest)
	require.Equal(t, "amd64", d.source.Architecture)
	require.Equal(t, substrate.GuestFamily("linux"), d.source.GuestFamily)
	require.WithinDuration(t, start.Add(5*time.Second), d.deadline, time.Second)
	require.Empty(t, d.lastSource.Source, "preflight must not call ResolveImage")
}

func TestImagePreflightOCINoInspectionSupport(t *testing.T) {
	d := &imageArtifactDriver{}
	registerImageArtifactDriver(t, d)
	checks := imagePreflightHCL(t, "example:latest", "image-test")
	require.Len(t, checks, 1)
	require.True(t, checks[0].OK)
	require.Equal(t, "warning", checks[0].Severity)
	require.Contains(t, checks[0].Message, "not support")
	require.Empty(t, d.lastSource.Source)
}

func TestImagePreflightOCIMissingDriver(t *testing.T) {
	registerImageArtifactDriver(t, &imageArtifactDriver{})
	checks := imagePreflightHCL(t, "example:latest", "not-registered")
	require.Len(t, checks, 1)
	require.False(t, checks[0].OK)
	require.Equal(t, "error", checks[0].Severity)
	require.Contains(t, checks[0].Message, "not-registered")
}

func TestImagePreflightOCIDefersSecrets(t *testing.T) {
	d := &preflightImageDriver{}
	registerImageArtifactDriver(t, d)
	reference := secret.Environment("PRIVATE_IMAGE").String()
	checks := imagePreflightHCL(t, reference, "image-test")
	require.Len(t, checks, 1)
	require.True(t, checks[0].OK)
	require.Equal(t, "warning", checks[0].Severity)
	require.Contains(t, checks[0].Message, "execution")
	require.NotContains(t, checks[0].Message, reference)
	require.NotContains(t, checks[0].Hint, reference)
	require.False(t, d.called)
}
