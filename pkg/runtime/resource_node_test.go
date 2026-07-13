package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
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
	name               string
	exposures          []string
	lastSpec           substrate.NodeSpec
	deletedAttachments []json.RawMessage
	natNames           []string
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

func (s *portTestSubstrate) Attach(context.Context, substrate.NodeHandle, driver.AttachmentRequest) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{Driver: s.name}, nil
}
func (s *portTestSubstrate) Observe(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{}, nil
}
func (s *portTestSubstrate) Delete(_ context.Context, _ substrate.NodeHandle, _ driver.AttachmentRequest, raw json.RawMessage) error {
	s.deletedAttachments = append(s.deletedAttachments, append(json.RawMessage(nil), raw...))
	return nil
}
func (s *portTestSubstrate) ApplyRuleset(_ context.Context, _ driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	s.natNames = []string{spec.NAT.SourceAttachment, spec.NAT.UplinkAttachment}
	return driver.RulesetObservation{Table: driver.RulesetTableName(spec.Owner), Digest: "digest"}, nil
}
func (s *portTestSubstrate) ObserveRuleset(context.Context, driver.PolicyTarget, string) (driver.RulesetObservation, error) {
	return driver.RulesetObservation{}, nil
}
func (s *portTestSubstrate) DeleteRuleset(context.Context, driver.PolicyTarget, string) error {
	return nil
}

func (s *portTestSubstrate) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}

func TestDestroyNodeDeletesTypedAttachments(t *testing.T) {
	sub := &portTestSubstrate{name: "delete-test"}
	registerPortTestDriver(t, sub)
	st := &state.State{Version: state.SchemaVersion}
	st.AddResource(state.Resource{Address: address.Resource("sysbox_network", "public"), Attributes: map[string]any{"nat": true, "docker_network_id": "net-1"}})
	resource := state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: sub.name, Attributes: map[string]any{"container_id": "node-id"}, Attachments: []state.Attachment{{Name: "uplink", Network: address.Resource("sysbox_network", "public"), Driver: sub.name, DriverState: json.RawMessage(`{"id":"opaque"}`)}}}
	st.AddResource(resource)
	exec := NewExecutor(graph.New(), st)
	require.NoError(t, exec.destroyNodeResource(context.Background(), resource))
	require.Len(t, sub.deletedAttachments, 1)
	require.JSONEq(t, `{"id":"opaque"}`, string(sub.deletedAttachments[0]))
}

func registerPortTestDriver(t *testing.T, sub *portTestSubstrate) {
	t.Helper()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{
		Name: sub.name, Version: "test", Node: sub, NIC: sub, NodeState: sub, Policy: sub,
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
	exec.state.AddResource(state.Resource{Address: address.Resource("sysbox_network", "public"), Driver: "docker", Attributes: map[string]any{"nat": true, "docker_network_id": "net-1"}})
	n := &graph.Node{
		Address: address.Resource("sysbox_node", "web"),
		Data: &config.NodeConfig{
			Image:     "sysbox_image.nginx.id",
			Substrate: "port-test",
			Links:     []config.LinkConfig{{Name: "uplink", Network: "sysbox_network.public", IP: "10.0.0.10/24"}},
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

func TestNodeResourceHandlerPreservesTypedProviderConfig(t *testing.T) {
	sub := &portTestSubstrate{name: "typed-config-test"}
	registerPortTestDriver(t, sub)
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	exec.state.AddResource(state.Resource{
		Address:    address.Resource("sysbox_image", "base"),
		Driver:     sub.name,
		Attributes: map[string]any{"image_id": "image-id", "repository": "base"},
	})
	type providerConfig struct{ Value string }
	provider := &providerConfig{Value: "preserved"}
	n := &graph.Node{
		Address: address.Resource("sysbox_node", "node"),
		Data: &config.NodeConfig{
			Image:          "sysbox_image.base.id",
			Substrate:      sub.name,
			ProviderConfig: provider,
		},
	}

	_, err := NodeResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Equal(t, provider, sub.lastSpec.ProviderConfig)
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
