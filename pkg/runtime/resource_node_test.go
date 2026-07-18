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
	require.Equal(t, 2, p.Schema().Version)
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
	guestInitModes     []substrate.GuestNetworkInitMode
	guestObservation   substrate.GuestNetworkInitObservation
	lifecycle          []string
	coldPlug           bool
	resetLifecycle     []string
	resetObservation   substrate.ResetObservation
	resetApplyErr      error
	nodeObservation    substrate.NodeObservation
	nodeObserveCalls   int
}

func (s *portTestSubstrate) Name() string { return s.name }

func (s *portTestSubstrate) Capabilities() substrate.Capabilities {
	return substrate.Capabilities{
		NICHotPlug:            !s.coldPlug,
		PortExposures:         s.exposures,
		GuestNetworkInitModes: s.guestInitModes,
	}
}

func (s *portTestSubstrate) ResolveImage(context.Context, substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	return substrate.ArtifactHandle{}, nil
}

func (s *portTestSubstrate) CreateNode(_ context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	s.lastSpec = spec
	return substrate.NodeHandle{
		ID:   "node-id",
		Conn: substrate.ConnInfo{Kind: substrate.ConnKindDocker, Endpoint: "node-id"},
	}, nil
}

func (s *portTestSubstrate) StartNode(context.Context, substrate.NodeHandle) error {
	s.lifecycle = append(s.lifecycle, "start")
	return nil
}

func (s *portTestSubstrate) StopNode(context.Context, substrate.NodeHandle) error { return nil }

func (s *portTestSubstrate) DestroyNode(context.Context, substrate.NodeHandle) error { return nil }

func (s *portTestSubstrate) Attach(context.Context, substrate.NodeHandle, driver.AttachmentRequest) (driver.AttachmentResult, error) {
	s.lifecycle = append(s.lifecycle, "attach")
	return driver.AttachmentResult{Driver: s.name}, nil
}

func (s *portTestSubstrate) PrepareGuestNetwork(context.Context, substrate.NodeHandle) error {
	s.lifecycle = append(s.lifecycle, "prepare_guest_network")
	return nil
}

func (s *portTestSubstrate) ObserveGuestNetwork(context.Context, substrate.NodeHandle) (substrate.GuestNetworkInitObservation, error) {
	s.lifecycle = append(s.lifecycle, "observe_guest_network")
	return s.guestObservation, nil
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

func (s *portTestSubstrate) PrepareReset(_ context.Context, request substrate.ResetRequest) (substrate.ResetHandle, error) {
	s.resetLifecycle = append(s.resetLifecycle, "prepare:"+request.Current.ID)
	return substrate.ResetHandle{Provider: map[string]string{"old_id": request.Current.ID}}, nil
}
func (s *portTestSubstrate) DestroyReset(context.Context, substrate.ResetHandle) error {
	s.resetLifecycle = append(s.resetLifecycle, "destroy")
	return nil
}
func (s *portTestSubstrate) ApplyReset(context.Context, substrate.ResetHandle) (substrate.NodeHandle, error) {
	s.resetLifecycle = append(s.resetLifecycle, "apply")
	if s.resetApplyErr != nil {
		return substrate.NodeHandle{}, s.resetApplyErr
	}
	return substrate.NodeHandle{ID: "node-reset"}, nil
}
func (s *portTestSubstrate) ObserveReset(context.Context, substrate.ResetHandle) (substrate.ResetObservation, error) {
	s.resetLifecycle = append(s.resetLifecycle, "observe")
	if s.resetObservation.Phase != "" || s.resetObservation.Reason != "" || len(s.resetObservation.Residue) > 0 {
		return s.resetObservation, nil
	}
	return substrate.ResetObservation{Phase: substrate.ResetPhaseComplete, Converged: true, NewExternalID: "node-reset"}, nil
}
func (s *portTestSubstrate) CleanupReset(context.Context, substrate.ResetHandle) error {
	s.resetLifecycle = append(s.resetLifecycle, "cleanup")
	return nil
}
func (s *portTestSubstrate) MarshalResetHandle(handle substrate.ResetHandle) (json.RawMessage, error) {
	return json.Marshal(handle.Provider)
}
func (s *portTestSubstrate) UnmarshalResetHandle(raw json.RawMessage) (substrate.ResetHandle, error) {
	var provider map[string]string
	if err := json.Unmarshal(raw, &provider); err != nil {
		return substrate.ResetHandle{}, err
	}
	return substrate.ResetHandle{Provider: provider}, nil
}

func (s *portTestSubstrate) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}

func (s *portTestSubstrate) ObserveNode(context.Context, substrate.NodeHandle) (substrate.NodeObservation, error) {
	s.nodeObserveCalls++
	if s.nodeObservation.Status != "" {
		return s.nodeObservation, nil
	}
	return substrate.NodeObservation{Exists: true, Running: true, Healthy: true, Status: substrate.NodeStatusRunning}, nil
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
		Name: sub.name, Version: "test", Node: sub, NIC: sub, NodeState: sub, Policy: sub, GuestNetworkInit: sub, Reset: sub,
	}))
}

func TestNodeResourceHandlerRunsGuestNetworkInitLifecycle(t *testing.T) {
	sub := &portTestSubstrate{
		name:             "guest-init-test",
		guestInitModes:   []substrate.GuestNetworkInitMode{substrate.GuestNetworkInitCloudInit},
		guestObservation: substrate.GuestNetworkInitObservation{Mode: substrate.GuestNetworkInitCloudInit, Converged: true},
		coldPlug:         true,
	}
	registerPortTestDriver(t, sub)
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	exec.state.AddResource(state.Resource{Address: address.Resource("sysbox_image", "base"), Driver: sub.name, Attributes: map[string]any{"image_id": "image-id", "repository": "base", "guest_family": "linux"}})
	exec.state.AddResource(state.Resource{Address: address.Resource("sysbox_network", "matrix"), Driver: "network", Attributes: map[string]any{"cidr": "10.44.0.0/24"}})
	n := &graph.Node{Address: address.Resource("sysbox_node", "node"), Data: &config.NodeConfig{
		Image: "sysbox_image.base.id", Substrate: sub.name,
		Links: []config.LinkConfig{{Name: "matrix", Network: "sysbox_network.matrix", IP: "10.44.0.30/24"}},
	}}

	resource, err := NodeResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.NoError(t, err)
	require.Equal(t, []string{"attach", "prepare_guest_network", "start", "observe_guest_network"}, sub.lifecycle)
	require.Equal(t, string(substrate.GuestNetworkInitCloudInit), resource.Str("guest_network_init_mode"))
	require.True(t, resource.Bool("guest_network_init_converged"))
	require.Equal(t, string(substrate.GuestFamilyLinux), resource.Str("guest_family"))
}

func TestNodeResourceHandlerRejectsNonConvergedGuestNetwork(t *testing.T) {
	sub := &portTestSubstrate{
		name:             "guest-init-fail-test",
		guestInitModes:   []substrate.GuestNetworkInitMode{substrate.GuestNetworkInitCloudInit},
		guestObservation: substrate.GuestNetworkInitObservation{Mode: substrate.GuestNetworkInitCloudInit, Converged: false, Reason: "address unavailable"},
		coldPlug:         true,
	}
	registerPortTestDriver(t, sub)
	exec := NewExecutor(graph.New(), &state.State{Version: state.SchemaVersion})
	exec.state.AddResource(state.Resource{Address: address.Resource("sysbox_image", "base"), Driver: sub.name, Attributes: map[string]any{"image_id": "image-id", "repository": "base", "guest_family": "linux"}})
	n := &graph.Node{Address: address.Resource("sysbox_node", "node"), Data: &config.NodeConfig{Image: "sysbox_image.base.id", Substrate: sub.name}}

	_, err := NodeResourceHandler{}.Create(context.Background(), &ProviderContext{exec: exec}, n)

	require.ErrorContains(t, err, "address unavailable")
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
			"image_id":     "image-id",
			"repository":   "nginx:alpine",
			"guest_family": "linux",
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
		Attributes: map[string]any{"image_id": "image-id", "repository": "base", "guest_family": "linux"},
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

func TestNodeProviderLaunchChangeRequiresReplacement(t *testing.T) {
	type optionalArgv struct {
		Set   bool     `json:"set"`
		Value []string `json:"value"`
	}
	type providerConfig struct {
		Command optionalArgv `json:"command"`
	}
	node := &graph.Node{
		Address: address.Resource("sysbox_node", "service"),
		Data: &config.NodeConfig{
			Image:          "sysbox_image.service.id",
			Substrate:      "docker",
			ProviderConfig: &providerConfig{Command: optionalArgv{Set: true, Value: []string{"serve"}}},
		},
	}
	attributes := map[string]any{}
	require.NoError(t, setDesiredHash(node, attributes))
	current := &state.Resource{Address: node.Address, Driver: "docker", Attributes: attributes}

	node.Data.(*config.NodeConfig).ProviderConfig = &providerConfig{Command: optionalArgv{Set: true, Value: []string{"serve", "--debug"}}}
	change, err := NodeResourceHandler{}.PlanDiff(node, current)

	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, change.Action)
	_, found := fieldChangeAt(change.Changes, "provider_config.command.value[1]")
	require.True(t, found)
}

func TestNodeAliasChangeRequiresReplacement(t *testing.T) {
	node := &graph.Node{
		Address: address.Resource("sysbox_node", "service"),
		Data: &config.NodeConfig{
			Image: "sysbox_image.service.id", Substrate: "docker",
			Links: []config.LinkConfig{{Name: "app", Network: "sysbox_network.app.id", IP: "10.0.0.10/24"}},
		},
	}
	attributes := map[string]any{}
	require.NoError(t, setDesiredHash(node, attributes))
	desired := attributes[desiredPayloadKey].(map[string]any)
	require.Equal(t, []string{"service"}, desired["links"].([]config.LinkConfig)[0].Aliases)
	current := &state.Resource{Address: node.Address, Driver: "docker", Attributes: attributes}

	node.Data.(*config.NodeConfig).Links[0].Aliases = []string{"database"}
	change, err := NodeResourceHandler{}.PlanDiff(node, current)

	require.NoError(t, err)
	require.Equal(t, controlplane.PlanActionReplace, change.Action)
	aliasChange, found := fieldChangeAt(change.Changes, "links[0].Aliases[1]")
	require.True(t, found)
	require.Equal(t, "database", aliasChange.After)
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
			"image_id":     "image-id",
			"repository":   "nginx:alpine",
			"guest_family": "linux",
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
