package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestNodeResourceProviderRegistered(t *testing.T) {
	p, ok := GetResourceProvider("sysbox_node")
	require.True(t, ok)
	require.Equal(t, "sysbox_node", p.Type())
	require.Equal(t, "sysbox_node", p.Schema().Type)
}

func TestNodeResourceProviderPlanDiff(t *testing.T) {
	n := &graph.Node{
		ID: graph.NodeID{Type: "sysbox_node", Name: "web"},
		Data: &config.NodeConfig{
			Image:     "sysbox_image.alpine.id",
			Substrate: "docker",
			Env:       map[string]string{"TOKEN": "old"},
		},
	}
	inst := map[string]any{}
	require.NoError(t, setDesiredHash(n, inst))
	current := &state.Resource{Type: "sysbox_node", Name: "web", Provider: "docker", Instance: inst}
	p := NodeResourceProvider{}

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
	require.True(t, action.Changes["env"].Sensitive)
}

func TestNodeResourceProviderDeleteMissingSubstrateReturnsError(t *testing.T) {
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	res := state.Resource{
		Type:     "sysbox_node",
		Name:     "web",
		Provider: "missing-node-provider",
		Instance: map[string]any{"container_id": "node"},
	}

	err := NodeResourceProvider{}.Delete(context.Background(), &ProviderContext{exec: exec}, res)
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

func TestNodeResourceProviderPortsArePassedAndResolved(t *testing.T) {
	sub := &portTestSubstrate{
		name:      "port-test",
		exposures: []string{substrate.PortExposureNone, substrate.PortExposureDirect, substrate.PortExposureHost},
	}
	substrate.Register(sub)
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	exec.state.AddResource(state.Resource{
		Type:     "sysbox_image",
		Name:     "nginx",
		Provider: "port-test",
		Instance: map[string]any{
			"image_id":   "image-id",
			"repository": "nginx:alpine",
		},
	})
	n := &graph.Node{
		ID: graph.NodeID{Type: "sysbox_node", Name: "web"},
		Data: &config.NodeConfig{
			Image:     "sysbox_image.nginx.id",
			Substrate: "port-test",
			Ports: []config.PortConfig{
				{Name: "http", Target: 80, Published: 28080, Protocol: "http", Exposure: "host", HostIP: "127.0.0.1"},
			},
		},
	}

	res, err := NodeResourceProvider{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Len(t, sub.lastSpec.Ports, 1)
	require.Equal(t, substrate.PortSpec{Name: "http", Target: 80, Published: 28080, Protocol: "http", Exposure: "host", HostIP: "127.0.0.1"}, sub.lastSpec.Ports[0])
	ports, ok := res.Instance["ports"].([]substrate.ResolvedPort)
	require.True(t, ok)
	require.Len(t, ports, 1)
	require.Equal(t, "http://127.0.0.1:28080", ports[0].URL)
	require.Equal(t, "host", ports[0].Exposure)
}

func TestNodeResourceProviderRejectsUnsupportedPortExposure(t *testing.T) {
	sub := &portTestSubstrate{
		name:      "port-direct-only",
		exposures: []string{substrate.PortExposureNone, substrate.PortExposureDirect},
	}
	substrate.Register(sub)
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	exec.state.AddResource(state.Resource{
		Type:     "sysbox_image",
		Name:     "nginx",
		Provider: "port-direct-only",
		Instance: map[string]any{
			"image_id":   "image-id",
			"repository": "nginx:alpine",
		},
	})
	n := &graph.Node{
		ID: graph.NodeID{Type: "sysbox_node", Name: "web"},
		Data: &config.NodeConfig{
			Image:     "sysbox_image.nginx.id",
			Substrate: "port-direct-only",
			Ports: []config.PortConfig{
				{Name: "http", Target: 80, Published: 28080, Protocol: "tcp", Exposure: "host"},
			},
		},
	}

	_, err := NodeResourceProvider{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.ErrorContains(t, err, `exposure "host" is not supported`)
}
