package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type observingNIC struct {
	result   driver.AttachmentResult
	err      error
	check    func(driver.AttachmentRequest) error
	identity func(context.Context)
}

func (o observingNIC) Attach(context.Context, substrate.NodeHandle, driver.AttachmentRequest) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{}, nil
}
func (o observingNIC) Observe(ctx context.Context, _ substrate.NodeHandle, req driver.AttachmentRequest, _ json.RawMessage) (driver.AttachmentResult, error) {
	if o.identity != nil {
		o.identity(ctx)
	}
	if o.check != nil {
		if err := o.check(req); err != nil {
			return driver.AttachmentResult{}, err
		}
	}
	return o.result, o.err
}
func (o observingNIC) Delete(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) error {
	return nil
}

func withObservingNIC(t *testing.T, nic driver.NIC) {
	t.Helper()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "test", Version: "1", NIC: nic}))
}

func attachmentResource() state.Resource {
	return state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "test", Attachments: []state.Attachment{{Name: "uplink", Node: address.Resource("sysbox_node", "web"), Network: address.Resource("sysbox_network", "public"), MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.0.0.10/24"}, Observation: state.AttachmentObservation{GuestDevice: "eth1"}, DriverState: json.RawMessage(`{"id":"old"}`)}}}
}

func attachmentTopology() *state.State {
	return &state.State{Resources: []state.Resource{{Address: address.Resource("sysbox_network", "public")}}}
}

func TestObserveAttachmentsUpdatesDeviceWithoutDrift(t *testing.T) {
	withObservingNIC(t, observingNIC{result: driver.AttachmentResult{GuestDevice: "eth7", State: json.RawMessage(`{"id":"new"}`)}})
	r := attachmentResource()
	status, _, err := observeAttachments(context.Background(), substrate.NodeHandle{ID: "node"}, &r, attachmentTopology(), driver.DefaultRegistry)
	require.NoError(t, err)
	require.Equal(t, state.ResourcePresent, status)
	require.Equal(t, "eth7", r.Attachments[0].Observation.GuestDevice)
	require.JSONEq(t, `{"id":"new"}`, string(r.Attachments[0].DriverState))
}

func TestObserveAttachmentsClassifiesNotFoundAndUnavailable(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		withObservingNIC(t, observingNIC{err: driver.Wrap(driver.ErrorNotFound, "test", "missing", nil)})
		r := attachmentResource()
		status, _, err := observeAttachments(context.Background(), substrate.NodeHandle{}, &r, attachmentTopology(), driver.DefaultRegistry)
		require.NoError(t, err)
		require.Equal(t, state.ResourceDrifted, status)
	})
	t.Run("unavailable", func(t *testing.T) {
		withObservingNIC(t, observingNIC{err: driver.Wrap(driver.ErrorUnavailable, "test", "offline", errors.New("down"))})
		r := attachmentResource()
		before := append([]byte(nil), r.Attachments[0].DriverState...)
		status, _, err := observeAttachments(context.Background(), substrate.NodeHandle{}, &r, attachmentTopology(), driver.DefaultRegistry)
		require.Error(t, err)
		require.Equal(t, state.ResourceUnknown, status)
		require.Equal(t, before, []byte(r.Attachments[0].DriverState))
	})
}

func TestResourceObservationIncludesCanonicalNetworkProof(t *testing.T) {
	for _, path := range []string{"refresh", "convergence", "health"} {
		t.Run(path, func(t *testing.T) {
			previous := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			t.Cleanup(func() { driver.DefaultRegistry = previous })
			network := state.Resource{Address: address.Resource("sysbox_network", "public"), OwnerUID: "owner", ResourceUID: "network-uid", Generation: 3, Attributes: state.MustAttributes(map[string]any{"nat": true})}
			require.NoError(t, network.SetRuntimeValue("docker_network_id", "network-id"))
			calls := 0
			nic := observingNIC{identity: func(ctx context.Context) {
				id, ok := substrate.OperationIdentityFromContext(ctx)
				require.True(t, ok)
				require.Equal(t, "owner", id.OwnerUID)
				require.Equal(t, "node-uid", id.ResourceUID)
				require.EqualValues(t, 2, id.Generation)
			}, check: func(req driver.AttachmentRequest) error {
				calls++
				require.Equal(t, attachmentNetworkIdentity(network), req.NetworkIdentity)
				var proof map[string]any
				require.NoError(t, json.Unmarshal(req.NetworkState, &proof))
				require.Equal(t, "network-id", proof["docker_network_id"])
				require.Equal(t, true, proof["nat"])
				return nil
			}}
			node := &portTestSubstrate{name: "test"}
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "test", Version: "1", Node: node, NodeState: node, NIC: nic}))
			r := attachmentResource()
			r.ExternalID = "node"
			r.OwnerUID, r.ResourceUID, r.Generation = "owner", "node-uid", 2
			st := &state.State{Resources: []state.Resource{network, r}}
			e := &Executor{state: st, providers: driver.DefaultRegistry}
			switch path {
			case "refresh":
				result, err := e.observeResource(context.Background(), r.Address)
				require.NoError(t, err)
				require.Equal(t, state.ResourcePresent, result.Status)
			case "convergence":
				result, err := e.observeTarget(context.Background(), r.Address)
				require.NoError(t, err)
				require.Equal(t, state.ResourcePresent, result.Status)
			case "health":
				EvaluateTopologyHealth(context.Background(), st, driver.DefaultRegistry)
			}
			require.Equal(t, 1, calls)
		})
	}
}

func TestAttachmentObservationFailsClosedWithoutCurrentNetworkProof(t *testing.T) {
	for _, scenario := range []string{"missing topology", "missing network", "replaced network"} {
		t.Run(scenario, func(t *testing.T) {
			previous := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			t.Cleanup(func() { driver.DefaultRegistry = previous })
			calls := 0
			node := &portTestSubstrate{name: "test"}
			nic := observingNIC{check: func(req driver.AttachmentRequest) error {
				calls++
				require.Equal(t, "replacement", req.NetworkIdentity.ResourceUID)
				return driver.Wrap(driver.ErrorInvalidState, "test", "network identity changed", nil)
			}}
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "test", Version: "1", Node: node, NodeState: node, NIC: nic}))
			r := attachmentResource()
			r.ExternalID = "node"
			before := cloneResetStateResource(r)
			var st *state.State
			if scenario != "missing topology" {
				st = &state.State{}
			}
			if scenario == "replaced network" {
				st = attachmentTopology()
				st.Resources[0].ResourceUID = "replacement"
			}
			result, err := readResourceWithAttachments(context.Background(), NodeResourceHandler{}, r, st, driver.DefaultRegistry)
			require.Error(t, err)
			require.Equal(t, state.ResourceUnknown, result.Status)
			require.Equal(t, before.Attachments, r.Attachments)
			if scenario == "replaced network" {
				require.Equal(t, 1, calls)
			} else {
				require.Zero(t, calls)
			}
			if scenario == "missing topology" {
				health := EvaluateResourceHealth(context.Background(), &r, driver.DefaultRegistry)
				require.Equal(t, model.ResourceHealthUnknown, health.Status)
				require.Contains(t, health.Reason, "topology state required")
			}
		})
	}
}

func TestResetObservesAttachmentsWithCanonicalNetworkProof(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	sub := &portTestSubstrate{name: "test", coldPlug: true}
	g, st := configuredResetTestState(t, sub, "first")
	id := address.Resource("sysbox_node", "first")
	network := state.Resource{Address: address.Resource("sysbox_network", "public"), OwnerUID: "network-owner", ResourceUID: "network-uid", Generation: 4}
	require.NoError(t, network.SetRuntimeValue("bridge", "persisted-bridge"))
	st.AddResource(network)
	g.Get(id).Data.(*config.NodeConfig).Links = []config.LinkConfig{{Name: "uplink", Network: network.Address.String(), IP: "10.0.0.10/24"}}
	calls := 0
	nic := observingNIC{check: func(req driver.AttachmentRequest) error {
		calls++
		require.Equal(t, attachmentNetworkIdentity(*st.FindResource(network.Address)), req.NetworkIdentity)
		var proof map[string]any
		require.NoError(t, json.Unmarshal(req.NetworkState, &proof))
		require.Equal(t, "persisted-bridge", proof["bridge"])
		return nil
	}}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: sub.name, Version: "test", Node: sub, NIC: nic, NodeState: sub, GuestNetwork: sub, GuestNetworkInit: sub, Reset: sub}))
	e := testActionExecutor(t, g, st)
	plan, err := BuildResetPlan(g, st, id.String())
	require.NoError(t, err)
	require.NoError(t, e.Reset(context.Background(), plan))
	require.GreaterOrEqual(t, calls, 2, "reset final health and candidate convergence both observe attachments")
}

func TestRouteDriftDoesNotMaskAttachmentUncertainty(t *testing.T) {
	for _, scenario := range []string{"missing network", "replaced network", "present network"} {
		t.Run(scenario, func(t *testing.T) {
			previous := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			t.Cleanup(func() { driver.DefaultRegistry = previous })
			node := &portTestSubstrate{name: "test"}
			calls := 0
			nic := observingNIC{check: func(req driver.AttachmentRequest) error {
				calls++
				if scenario == "replaced network" {
					return driver.Wrap(driver.ErrorInvalidState, "test", "network identity changed", nil)
				}
				return nil
			}}
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "test", Version: "1", Node: node, NodeState: node, NIC: nic, GuestNetwork: routeObservationProbe{exists: false}}))
			r := attachmentResource()
			r.ExternalID = "node"
			r.Attributes = state.MustAttributes(map[string]any{"routes": []any{map[string]any{"dst": "10.20.0.0/16", "via": "10.0.0.1"}}})
			st := attachmentTopology()
			if scenario == "missing network" {
				st.Resources = nil
			}
			st.AddResource(r)
			result, err := (&Executor{state: st, providers: driver.DefaultRegistry}).observeTarget(context.Background(), r.Address)
			if scenario == "present network" {
				require.NoError(t, err)
				require.Equal(t, state.ResourceDrifted, result.Status)
				require.Equal(t, "route missing", result.Reason)
				require.Equal(t, model.RecoveryDecisionMarkDrift, result.Decision)
			} else {
				require.ErrorIs(t, err, ErrObservationUnknown)
				require.Equal(t, state.ResourceUnknown, result.Status)
				require.Equal(t, model.RecoveryDecisionUnknown, result.Decision)
				action, _ := planActionForObservedStatus(result.Status)
				require.Equal(t, model.PlanActionUnknown, action)
			}
			if scenario == "missing network" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}
