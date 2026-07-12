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
)

func TestKernelResourceProviderCreateAndDelete(t *testing.T) {
	src := filepath.Join(t.TempDir(), "vmlinux")
	require.NoError(t, os.WriteFile(src, []byte("kernel"), 0o644))
	n := &graph.Node{
		Address: address.Resource("sysbox_kernel", "fc"),
		Data: &config.KernelConfig{
			Substrate: "firecracker",
			Source:    src,
		},
	}
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	p := KernelResourceProvider{}

	res, err := p.Create(context.Background(), &ProviderContext{exec: exec}, n)
	require.NoError(t, err)
	require.Equal(t, "sysbox_kernel", res.Address.Type)
	require.Equal(t, "fc", res.Address.Name)
	require.Equal(t, "firecracker", res.Driver)
	require.Equal(t, src, res.Str("path"))
	require.Equal(t, src, res.Str("source"))
	require.NotEmpty(t, res.Str("sha256"))
	require.NotEmpty(t, res.Str(desiredHashKey))

	exec.state.AddResource(res)
	require.NoError(t, p.Delete(context.Background(), &ProviderContext{exec: exec}, res))
	require.Nil(t, exec.state.FindResource(address.Resource("sysbox_kernel", "fc")))
	_, err = os.Stat(src)
	require.NoError(t, err)
}

func TestKernelResourceProviderPlanDiff(t *testing.T) {
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
	p := KernelResourceProvider{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.KernelConfig{Substrate: "firecracker", Source: "/tmp/vmlinux-b"}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	require.Contains(t, action.Changes, "source")
}

func TestKernelResourceProviderRegistered(t *testing.T) {
	p, ok := GetResourceProvider("sysbox_kernel")
	require.True(t, ok)
	require.Equal(t, "sysbox_kernel", p.Type())
	require.Equal(t, "sysbox_kernel", p.Schema().Type)
}
