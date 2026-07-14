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
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
)

func TestKernelResourceHandlerCreateAndDelete(t *testing.T) {
	src := filepath.Join(t.TempDir(), "vmlinux")
	require.NoError(t, os.WriteFile(src, []byte("kernel"), 0o644))
	n := &graph.Node{
		Address: address.Resource("sysbox_kernel", "fc"),
		Data: &config.KernelConfig{
			Substrate:    "firecracker",
			Source:       src,
			Architecture: "amd64",
		},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	p := KernelResourceHandler{}

	res, err := p.Create(context.Background(), &ProviderContext{exec: exec}, n)
	require.NoError(t, err)
	require.Equal(t, "sysbox_kernel", res.Address.Type)
	require.Equal(t, "fc", res.Address.Name)
	require.Equal(t, "firecracker", res.Driver)
	require.Equal(t, src, res.Str("path"))
	require.Equal(t, src, res.Str("source"))
	require.NotEmpty(t, res.Str("sha256"))
	require.Equal(t, "kernel", res.Str("kind"))
	require.Equal(t, "amd64", res.Str("architecture"))
	require.NotEmpty(t, res.Str(desiredHashKey))

	exec.state.AddResource(res)
	require.NoError(t, p.Delete(context.Background(), &ProviderContext{exec: exec}, res))
	require.Nil(t, exec.state.FindResource(address.Resource("sysbox_kernel", "fc")))
	_, err = os.Stat(src)
	require.NoError(t, err)
}

func TestKernelResourceHandlerResolvesSourceSecretReferenceAtExecution(t *testing.T) {
	src := filepath.Join(t.TempDir(), "vmlinux")
	require.NoError(t, os.WriteFile(src, []byte("kernel"), 0o644))
	previousResolver := executionSecretResolver
	executionSecretResolver = secret.EnvironmentResolver{Lookup: func(name string) (string, bool) {
		return src, name == "SYSBOX_KERNEL"
	}}
	t.Cleanup(func() { executionSecretResolver = previousResolver })
	reference := secret.Environment("SYSBOX_KERNEL").String()
	n := &graph.Node{
		Address: address.Resource("sysbox_kernel", "fc"),
		Data:    &config.KernelConfig{Substrate: "firecracker", Source: reference, Architecture: "amd64"},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})

	res, err := KernelResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Equal(t, src, res.Str("path"))
	require.Equal(t, reference, res.Str("source"))
}

func TestKernelResourceHandlerPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_kernel", "fc"),
		Data: &config.KernelConfig{
			Substrate: "firecracker",
			Source:    "/tmp/vmlinux-a",
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{
		Address:    address.Resource("sysbox_kernel", "fc"),
		Driver:     "firecracker",
		Attributes: inst,
	}
	p := KernelResourceHandler{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.KernelConfig{Substrate: "firecracker", Source: "/tmp/vmlinux-b"}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	_, ok := fieldChangeAt(action.Changes, "source")
	require.True(t, ok)
}

func TestKernelResourceHandlerRegistered(t *testing.T) {
	p, ok := GetResourceHandler("sysbox_kernel")
	require.True(t, ok)
	require.Equal(t, "sysbox_kernel", p.Type())
	require.Equal(t, "sysbox_kernel", p.Schema().Type)
}
