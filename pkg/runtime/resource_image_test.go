package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type imageArtifactDriver struct {
	lastSpec substrate.ImageSpec
}

func (s *imageArtifactDriver) PrepareImage(_ context.Context, spec substrate.ImageSpec) (substrate.ImageRef, error) {
	s.lastSpec = spec
	repo := spec.DockerRef
	if repo == "" {
		repo = spec.Rootfs + spec.QCow2
	}
	return substrate.ImageRef{ID: "image-id", Repository: repo}, nil
}

func registerImageArtifactDriver(t *testing.T, artifactDriver driver.Artifact) {
	t.Helper()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{
		Name: "image-test", Version: "test", Artifact: artifactDriver,
	}))
}

func TestImageResourceHandlerCreateOCIArtifact(t *testing.T) {
	sub := &imageArtifactDriver{}
	registerImageArtifactDriver(t, sub)
	n := &graph.Node{
		Address: address.Resource("sysbox_image", "alpine"),
		Data: &config.ImageConfig{
			Substrate: "image-test", Kind: "oci", Source: "alpine:latest",
			Architecture: "amd64", GuestFamily: "linux",
		},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})

	res, err := ImageResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Equal(t, "sysbox_image", res.Address.Type)
	require.Equal(t, "alpine", res.Address.Name)
	require.Equal(t, "image-test", res.Driver)
	require.Equal(t, "image-id", res.ImageID())
	require.Equal(t, "alpine:latest", res.Repository())
	require.Equal(t, "alpine:latest", sub.lastSpec.DockerRef)
	require.Equal(t, "linux", res.Str("guest_family"))
	require.NotEmpty(t, res.Str(desiredHashKey))
}

func TestImageResourceHandlerCreateRootfsArtifact(t *testing.T) {
	sub := &imageArtifactDriver{}
	registerImageArtifactDriver(t, sub)
	rootfs := filepath.Join(t.TempDir(), "rootfs.ext4")
	require.NoError(t, os.WriteFile(rootfs, []byte("rootfs"), 0o644))
	n := &graph.Node{
		Address: address.Resource("sysbox_image", "rootfs"),
		Data: &config.ImageConfig{
			Substrate: "image-test", Kind: "rootfs", Source: rootfs,
			Architecture: "amd64", GuestFamily: "linux",
		},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})

	res, err := ImageResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Equal(t, rootfs, sub.lastSpec.Rootfs)
	require.Equal(t, rootfs, res.Repository())
	require.Equal(t, rootfs, res.Str("source"))
	require.NotEmpty(t, res.Str("sha256"))
	require.Equal(t, "linux", res.Str("guest_family"))
}

func TestImageResourceHandlerResolvesRootfsSecretReferenceAtExecution(t *testing.T) {
	sub := &imageArtifactDriver{}
	registerImageArtifactDriver(t, sub)
	rootfs := filepath.Join(t.TempDir(), "rootfs.ext4")
	require.NoError(t, os.WriteFile(rootfs, []byte("rootfs"), 0o644))
	previousResolver := executionSecretResolver
	executionSecretResolver = secret.EnvironmentResolver{Lookup: func(name string) (string, bool) {
		return rootfs, name == "SYSBOX_ROOTFS"
	}}
	t.Cleanup(func() { executionSecretResolver = previousResolver })
	reference := secret.Environment("SYSBOX_ROOTFS").String()
	n := &graph.Node{
		Address: address.Resource("sysbox_image", "rootfs"),
		Data:    &config.ImageConfig{Substrate: "image-test", Kind: "rootfs", Source: reference, Architecture: "amd64", GuestFamily: "linux"},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})

	res, err := ImageResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Equal(t, rootfs, sub.lastSpec.Rootfs)
	require.Equal(t, reference, res.Str("source"))
}

func TestImageResourceHandlerDelete(t *testing.T) {
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	res := state.Resource{Address: address.Resource("sysbox_image", "alpine"), Driver: "image-test", Attributes: map[string]any{}}
	exec.state.AddResource(res)

	require.NoError(t, ImageResourceHandler{}.Delete(context.Background(), &ProviderContext{exec: exec}, res))
	require.Nil(t, exec.state.FindResource(address.Resource("sysbox_image", "alpine")))
}

func TestImageResourceHandlerPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_image", "alpine"),
		Data:    &config.ImageConfig{Substrate: "docker", Kind: "oci", Source: "alpine:3.20", Architecture: "amd64", GuestFamily: "linux"},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_image", "alpine"), Driver: "docker", Attributes: inst}
	p := ImageResourceHandler{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.ImageConfig{Substrate: "docker", Kind: "oci", Source: "alpine:3.21", Architecture: "amd64", GuestFamily: "linux"}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	_, ok := fieldChangeAt(action.Changes, "source")
	require.True(t, ok)
}
