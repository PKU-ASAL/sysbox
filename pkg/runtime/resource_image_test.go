package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type imageProviderSubstrate struct {
	substrate.BaseSubstrate
	lastSpec substrate.ImageSpec
}

func (s *imageProviderSubstrate) Name() string { return "image-test" }

func (s *imageProviderSubstrate) Capabilities() substrate.Capabilities {
	return substrate.Capabilities{}
}

func (s *imageProviderSubstrate) PrepareImage(_ context.Context, spec substrate.ImageSpec) (substrate.ImageRef, error) {
	s.lastSpec = spec
	repo := spec.DockerRef
	if repo == "" {
		repo = spec.Rootfs + spec.QCow2
	}
	return substrate.ImageRef{ID: "image-id", Repository: repo}, nil
}

func (*imageProviderSubstrate) CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, nil
}

func (*imageProviderSubstrate) StartNode(context.Context, substrate.NodeHandle) error { return nil }

func (*imageProviderSubstrate) StopNode(context.Context, substrate.NodeHandle) error { return nil }

func (*imageProviderSubstrate) DestroyNode(context.Context, substrate.NodeHandle) error { return nil }

func (*imageProviderSubstrate) AttachNIC(context.Context, substrate.NodeHandle, substrate.LinkRequest) (substrate.AttachedNIC, error) {
	return substrate.AttachedNIC{}, nil
}

func (*imageProviderSubstrate) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}

func TestImageResourceProviderCreateDockerRef(t *testing.T) {
	sub := &imageProviderSubstrate{}
	substrate.Register(sub)
	n := &graph.Node{
		Address: address.Resource("sysbox_image", "alpine"),
		Data: &config.ImageConfig{
			Substrate: "image-test",
			DockerRef: "alpine:latest",
		},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})

	res, err := ImageResourceProvider{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Equal(t, "sysbox_image", res.Address.Type)
	require.Equal(t, "alpine", res.Address.Name)
	require.Equal(t, "image-test", res.Provider)
	require.Equal(t, "image-id", res.ImageID())
	require.Equal(t, "alpine:latest", res.Repository())
	require.Equal(t, "alpine:latest", sub.lastSpec.DockerRef)
	require.NotEmpty(t, res.Str(desiredHashKey))
}

func TestImageResourceProviderCreateRootfsArtifact(t *testing.T) {
	sub := &imageProviderSubstrate{}
	substrate.Register(sub)
	rootfs := filepath.Join(t.TempDir(), "rootfs.ext4")
	require.NoError(t, os.WriteFile(rootfs, []byte("rootfs"), 0o644))
	n := &graph.Node{
		Address: address.Resource("sysbox_image", "rootfs"),
		Data: &config.ImageConfig{
			Substrate: "image-test",
			Rootfs:    rootfs,
		},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})

	res, err := ImageResourceProvider{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Equal(t, rootfs, sub.lastSpec.Rootfs)
	require.Equal(t, rootfs, res.Repository())
	require.Equal(t, rootfs, res.Str("source"))
	require.NotEmpty(t, res.Str("sha256"))
}

func TestImageResourceProviderDelete(t *testing.T) {
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	res := state.Resource{Address: address.Resource("sysbox_image", "alpine"), Provider: "image-test", Instance: map[string]any{}}
	exec.state.AddResource(res)

	require.NoError(t, ImageResourceProvider{}.Delete(context.Background(), &ProviderContext{exec: exec}, res))
	require.Nil(t, exec.state.FindResource(address.Resource("sysbox_image", "alpine")))
}

func TestImageResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_image", "alpine"),
		Data:    &config.ImageConfig{Substrate: "docker", DockerRef: "alpine:3.20"},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_image", "alpine"), Provider: "docker", Instance: inst}
	p := ImageResourceProvider{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.ImageConfig{Substrate: "docker", DockerRef: "alpine:3.21"}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	require.Contains(t, action.Changes, "docker_ref")
}
