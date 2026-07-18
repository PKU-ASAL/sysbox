package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type hookSubstrate struct {
	substrate.BaseSubstrate
}

func (hookSubstrate) Name() string { return "hook" }

func (hookSubstrate) Capabilities() substrate.Capabilities { return substrate.Capabilities{} }

func (hookSubstrate) ResolveImage(context.Context, substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	return substrate.ArtifactHandle{}, nil
}

func (hookSubstrate) CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, nil
}

func (hookSubstrate) StartNode(context.Context, substrate.NodeHandle) error { return nil }

func (hookSubstrate) StopNode(context.Context, substrate.NodeHandle) error { return nil }

func (hookSubstrate) DestroyNode(context.Context, substrate.NodeHandle) error { return nil }

func (hookSubstrate) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}

func (hookSubstrate) Attach(_ context.Context, _ substrate.NodeHandle, req driver.AttachmentRequest) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{Driver: "hook", GuestDevice: req.Name, State: json.RawMessage(`{"id":"x"}`)}, nil
}
func (hookSubstrate) Observe(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{}, nil
}
func (hookSubstrate) Delete(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) error {
	return nil
}

func TestWireNICsWithHookRecordsAttachPhases(t *testing.T) {
	st := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{
			{
				Address: address.Resource("sysbox_network", "nat"),
				Driver:  "docker",
				Attributes: map[string]any{
					"nat":               true,
					"docker_network_id": "net-1",
				},
			},
			{
				Address: address.Resource("sysbox_network", "isolated"),
				Driver:  "network",
				Attributes: map[string]any{
					"netns":  "sysbox-net-isolated",
					"bridge": "br-isolated",
				},
			},
		},
	}
	var phases []string
	hook := func(phase string, _ map[string]any, fn func() error) error {
		phases = append(phases, phase)
		return fn()
	}

	owner := address.Resource("sysbox_node", "node")
	result, err := wireNICsWithHook(context.Background(), hookSubstrate{}, st, substrate.NodeHandle{ID: "node"}, []NICSpec{
		{Name: "uplink", Network: "nat", IP: "172.20.0.10"},
		{Name: "internal", Network: "isolated", IP: "10.10.0.10/24"},
	}, owner, hook)

	require.NoError(t, err)
	require.Equal(t, []string{"attach", "attach"}, phases)
	require.Len(t, result.Attachments, 2)
	require.Equal(t, "172.20.0.10", result.PrimaryIP)
}

func TestWireNICsPassesNormalizedMACToDriver(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{
		Address: address.Resource("sysbox_network", "isolated"),
		Driver:  "network",
		Attributes: map[string]any{
			"netns": "sysbox-net-isolated", "bridge": "br-isolated",
		},
	}}}
	var requests []driver.AttachmentRequest
	driver := recordingNIC{requests: &requests}

	_, err := wireNICs(context.Background(), driver, st, substrate.NodeHandle{ID: "node"}, []NICSpec{{
		Name: "internal", Network: "isolated", IP: "10.10.0.10/24", MAC: "02:00:00:00:00:01",
	}}, address.Resource("sysbox_node", "node"))

	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(t, "02:00:00:00:00:01", requests[0].MAC)
}

func TestWireNICsPersistsAliasesOnNATNetwork(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{
		Address:    address.Resource("sysbox_network", "app"),
		Attributes: map[string]any{"nat": true, "docker_network_id": "net-1"},
	}}}
	var requests []driver.AttachmentRequest

	result, err := wireNICs(context.Background(), recordingNIC{requests: &requests}, st, substrate.NodeHandle{ID: "node", Conn: substrate.ConnInfo{Kind: substrate.ConnKindDocker}}, []NICSpec{{
		Name: "app", Network: "app", IP: "10.10.0.10/24", Aliases: []string{"web", "frontend"}, AliasesExplicit: true,
	}}, address.Resource("sysbox_node", "web"))

	require.NoError(t, err)
	require.Equal(t, []string{"web", "frontend"}, requests[0].Aliases)
	require.Equal(t, []string{"web", "frontend"}, result.Attachments[0].Aliases)
}

func TestWireNICsOmitsAutomaticAliasForVMOnNATNetwork(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{
		Address:    address.Resource("sysbox_network", "app"),
		Attributes: map[string]any{"nat": true, "docker_network_id": "net-1"},
	}}}
	var requests []driver.AttachmentRequest

	_, err := wireNICs(context.Background(), recordingNIC{requests: &requests}, st, substrate.NodeHandle{ID: "vm", Conn: substrate.ConnInfo{Kind: substrate.ConnKindSSH}}, []NICSpec{{
		Name: "app", Network: "app", IP: "10.10.0.10/24", Aliases: []string{"vm"},
	}}, address.Resource("sysbox_node", "vm"))

	require.NoError(t, err)
	require.Empty(t, requests[0].Aliases)
}

func TestWireNICsOmitsAutomaticAliasOnIsolatedNetwork(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{
		Address: address.Resource("sysbox_network", "isolated"),
	}}}
	var requests []driver.AttachmentRequest

	result, err := wireNICs(context.Background(), recordingNIC{requests: &requests}, st, substrate.NodeHandle{ID: "node"}, []NICSpec{{
		Name: "internal", Network: "isolated", IP: "10.10.0.10/24", Aliases: []string{"web"},
	}}, address.Resource("sysbox_node", "web"))

	require.NoError(t, err)
	require.Empty(t, requests[0].Aliases)
	require.Empty(t, result.Attachments[0].Aliases)
}

func TestWireNICsRejectsExplicitAliasOnIsolatedNetworkBeforeAttach(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{
		Address: address.Resource("sysbox_network", "isolated"),
	}}}
	var requests []driver.AttachmentRequest

	_, err := wireNICs(context.Background(), recordingNIC{requests: &requests}, st, substrate.NodeHandle{ID: "node"}, []NICSpec{{
		Name: "internal", Network: "isolated", IP: "10.10.0.10/24", Aliases: []string{"web", "frontend"}, AliasesExplicit: true,
	}}, address.Resource("sysbox_node", "web"))

	require.ErrorContains(t, err, "network aliases require a Docker-managed network")
	require.Empty(t, requests)
}

func TestWireNICsPreflightsAllAliasesBeforeAnyAttach(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{
		{Address: address.Resource("sysbox_network", "app"), Attributes: map[string]any{"nat": true, "docker_network_id": "net-1"}},
		{Address: address.Resource("sysbox_network", "isolated")},
	}}
	var requests []driver.AttachmentRequest

	_, err := wireNICs(context.Background(), recordingNIC{requests: &requests}, st, substrate.NodeHandle{ID: "node", Conn: substrate.ConnInfo{Kind: substrate.ConnKindDocker}}, []NICSpec{
		{Name: "app", Network: "app", IP: "10.10.0.10/24", Aliases: []string{"web"}},
		{Name: "internal", Network: "isolated", IP: "192.0.2.10/24", Aliases: []string{"web", "frontend"}, AliasesExplicit: true},
	}, address.Resource("sysbox_node", "web"))

	require.ErrorContains(t, err, "network aliases require a Docker-managed network")
	require.Empty(t, requests)
}

func TestWireNICsPassesNetworkRuntimeStateToDriver(t *testing.T) {
	network := state.Resource{Address: address.Resource("sysbox_network", "isolated"), Driver: "network", Attributes: state.MustAttributes(map[string]any{"cidr": "10.10.0.0/24"})}
	require.NoError(t, network.SetAttribute("netns", "sysbox-net-isolated"))
	require.NoError(t, network.SetAttribute("bridge", "br-isolated"))
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{network}}
	var requests []driver.AttachmentRequest

	_, err := wireNICs(context.Background(), recordingNIC{requests: &requests}, st, substrate.NodeHandle{ID: "node"}, []NICSpec{{
		Name: "internal", Network: "isolated", IP: "10.10.0.10/24",
	}}, address.Resource("sysbox_node", "node"))

	require.NoError(t, err)
	require.Len(t, requests, 1)
	var got map[string]any
	require.NoError(t, json.Unmarshal(requests[0].NetworkState, &got))
	require.Equal(t, "10.10.0.0/24", got["cidr"])
	require.Equal(t, "sysbox-net-isolated", got["netns"])
	require.Equal(t, "br-isolated", got["bridge"])

	recovered, err := attachmentRequestFromState(st, state.Attachment{Name: "internal", Network: network.Address, Aliases: []string{"web", "frontend"}})
	require.NoError(t, err)
	require.JSONEq(t, string(requests[0].NetworkState), string(recovered.NetworkState))
	require.Equal(t, []string{"web", "frontend"}, recovered.Aliases)
}

type recordingNIC struct {
	requests *[]driver.AttachmentRequest
}

func (s recordingNIC) Attach(_ context.Context, _ substrate.NodeHandle, req driver.AttachmentRequest) (driver.AttachmentResult, error) {
	*s.requests = append(*s.requests, req)
	return driver.AttachmentResult{Driver: "recording", GuestDevice: req.Name}, nil
}
func (s recordingNIC) Observe(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{}, nil
}
func (s recordingNIC) Delete(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) error {
	return nil
}
