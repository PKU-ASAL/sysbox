package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestNodeResourceHandlerRegistered(t *testing.T) {
	p, ok := GetResourceHandler("sysbox_node")
	require.True(t, ok)
	require.Equal(t, "sysbox_node", p.Type())
	require.Equal(t, "sysbox_node", p.Schema().Type)
}

func TestNodeResourceHandlerPlanDiff(t *testing.T) {
	n := &graph.Node{
		Address: address.Resource("sysbox_node", "web"),
		Data: &config.NodeConfig{
			Image:     "sysbox_image.alpine.id",
			Substrate: "docker",
			Env:       map[string]string{"TOKEN": "old"},
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "docker", Attributes: inst}
	p := NodeResourceHandler{}

	action, err := p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionNoop, action.Action)

	n.Data = &config.NodeConfig{
		Image:     "sysbox_image.alpine.id",
		Substrate: "docker",
		Env:       map[string]string{"TOKEN": "new"},
	}
	action, err = p.PlanDiff(n, current)
	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, action.Action)
	change, ok := fieldChangeAt(action.Changes, "env.TOKEN")
	require.True(t, ok)
	require.True(t, change.Sensitive)
}

func TestNodeResourceHandlerDeleteMissingSubstrateReturnsError(t *testing.T) {
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	res := state.Resource{
		Address:    address.Resource("sysbox_node", "web"),
		Driver:     "missing-node-provider",
		Attributes: map[string]any{"container_id": "node"},
	}

	err := NodeResourceHandler{}.Delete(context.Background(), &ProviderContext{exec: exec}, res)
	require.Error(t, err)
}

type portTestSubstrate struct {
	substrate.BaseSubstrate
	name      string
	exposures []string
	lastSpec  substrate.NodeSpec
}

func (s *portTestSubstrate) Name() string { return s.name }

func (s *portTestSubstrate) Capabilities() substrate.Capabilities {
	return substrate.Capabilities{
		NICHotPlug:    true,
		PortExposures: s.exposures,
	}
}

func (s *portTestSubstrate) PrepareImage(context.Context, substrate.ImageSpec) (substrate.ImageRef, error) {
	return substrate.ImageRef{}, nil
}

func (s *portTestSubstrate) CreateNode(_ context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	s.lastSpec = spec
	return substrate.NodeHandle{
		ID:   "node-id",
		Conn: substrate.ConnInfo{Kind: substrate.ConnKindDocker, Endpoint: "node-id"},
	}, nil
}

func (s *portTestSubstrate) StartNode(context.Context, substrate.NodeHandle) error { return nil }

func (s *portTestSubstrate) StopNode(context.Context, substrate.NodeHandle) error { return nil }

func (s *portTestSubstrate) DestroyNode(context.Context, substrate.NodeHandle) error { return nil }

func (s *portTestSubstrate) AttachNIC(context.Context, substrate.NodeHandle, substrate.LinkRequest) (substrate.AttachedNIC, error) {
	return substrate.AttachedNIC{}, nil
}

func (s *portTestSubstrate) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}

func registerPortTestDriver(t *testing.T, sub *portTestSubstrate) {
	t.Helper()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{
		Name: sub.name, Version: "test", Node: sub, NIC: sub, NodeState: sub,
	}))
}

func TestNodeResourceHandlerPortsArePassedAndResolved(t *testing.T) {
	sub := &portTestSubstrate{
		name:      "port-test",
		exposures: []string{substrate.PortExposureNone, substrate.PortExposureDirect, substrate.PortExposureHost},
	}
	registerPortTestDriver(t, sub)
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	exec.state.AddResource(state.Resource{
		Address: address.Resource("sysbox_image", "nginx"),
		Driver:  "port-test",
		Attributes: map[string]any{
			"image_id":   "image-id",
			"repository": "nginx:alpine",
		},
	})
	n := &graph.Node{
		Address: address.Resource("sysbox_node", "web"),
		Data: &config.NodeConfig{
			Image:     "sysbox_image.nginx.id",
			Substrate: "port-test",
			Ports: []config.PortConfig{
				{Name: "http", Target: 80, Published: 28080, Protocol: "http", Exposure: "host", HostIP: "127.0.0.1"},
			},
		},
	}

	res, err := NodeResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Len(t, sub.lastSpec.Ports, 1)
	require.Equal(t, substrate.PortSpec{Name: "http", Target: 80, Published: 28080, Protocol: "http", Exposure: "host", HostIP: "127.0.0.1"}, sub.lastSpec.Ports[0])
	ports, ok := res.Attributes["ports"].([]any)
	require.True(t, ok)
	require.Len(t, ports, 1)
	resolved, ok := ports[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:28080", resolved["url"])
	require.Equal(t, "host", resolved["exposure"])
}

func TestNodeResourceHandlerRejectsUnsupportedPortExposure(t *testing.T) {
	sub := &portTestSubstrate{
		name:      "port-direct-only",
		exposures: []string{substrate.PortExposureNone, substrate.PortExposureDirect},
	}
	registerPortTestDriver(t, sub)
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	exec.state.AddResource(state.Resource{
		Address: address.Resource("sysbox_image", "nginx"),
		Driver:  "port-direct-only",
		Attributes: map[string]any{
			"image_id":   "image-id",
			"repository": "nginx:alpine",
		},
	})
	n := &graph.Node{
		Address: address.Resource("sysbox_node", "web"),
		Data: &config.NodeConfig{
			Image:     "sysbox_image.nginx.id",
			Substrate: "port-direct-only",
			Ports: []config.PortConfig{
				{Name: "http", Target: 80, Published: 28080, Protocol: "tcp", Exposure: "host"},
			},
		},
	}

	_, err := NodeResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.ErrorContains(t, err, `exposure "host" is not supported`)
}
