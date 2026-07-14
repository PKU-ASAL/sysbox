package driver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type fakeNodeDriver struct{ substrate.BaseSubstrate }

func (*fakeNodeDriver) Capabilities() substrate.Capabilities { return substrate.Capabilities{} }

func (*fakeNodeDriver) CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, nil
}
func (*fakeNodeDriver) StartNode(context.Context, substrate.NodeHandle) error   { return nil }
func (*fakeNodeDriver) StopNode(context.Context, substrate.NodeHandle) error    { return nil }
func (*fakeNodeDriver) DestroyNode(context.Context, substrate.NodeHandle) error { return nil }
func (*fakeNodeDriver) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}
func (*fakeNodeDriver) ObserveNode(context.Context, substrate.NodeHandle) (substrate.NodeObservation, error) {
	return substrate.NodeObservation{}, nil
}
func (*fakeNodeDriver) AdoptNode(_ context.Context, handle substrate.NodeHandle) (substrate.NodeHandle, error) {
	return handle, nil
}

func TestRegistryRejectsDuplicateDriverIdentity(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.Register(Descriptor{Name: "docker", Version: "1", Node: &fakeNodeDriver{}}))
	require.ErrorContains(t, registry.Register(Descriptor{Name: "docker", Version: "2"}), "already registered")
}

func TestRegistryRequiresDeclaredCapability(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.Register(Descriptor{Name: "docker", Version: "1", Node: &fakeNodeDriver{}}))
	_, err := registry.Require("docker", CapabilityNetwork)
	var driverError *Error
	require.True(t, errors.As(err, &driverError))
	require.Equal(t, ErrorUnsupported, driverError.Category)

	descriptor, err := registry.Require("docker", CapabilityNode)
	require.NoError(t, err)
	require.NotNil(t, descriptor.Node)
}

func TestRegistryReturnsTypedCapability(t *testing.T) {
	registry := NewRegistry()
	node := &fakeNodeDriver{}
	require.NoError(t, registry.Register(Descriptor{Name: "docker", Version: "1", Node: node}))

	got, err := registry.RequireNode("docker")
	require.NoError(t, err)
	require.Same(t, node, got)
}

type fakeGuestNetworkInit struct{}

func (fakeGuestNetworkInit) PrepareGuestNetwork(context.Context, substrate.NodeHandle) error {
	return nil
}

type fakeReset struct{}

func (fakeReset) PrepareReset(context.Context, substrate.ResetRequest) (substrate.ResetHandle, error) {
	return substrate.ResetHandle{}, nil
}

func (fakeReset) ApplyReset(context.Context, substrate.ResetHandle) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, nil
}

func (fakeReset) ObserveReset(context.Context, substrate.ResetHandle) (substrate.ResetObservation, error) {
	return substrate.ResetObservation{Phase: substrate.ResetPhaseComplete, Converged: true}, nil
}

func (fakeReset) CleanupReset(context.Context, substrate.ResetHandle) error { return nil }
func (fakeReset) MarshalResetHandle(substrate.ResetHandle) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (fakeReset) UnmarshalResetHandle(json.RawMessage) (substrate.ResetHandle, error) {
	return substrate.ResetHandle{}, nil
}

func TestRegistryRequiresResetCapability(t *testing.T) {
	registry := NewRegistry()
	reset := fakeReset{}
	require.NoError(t, registry.Register(Descriptor{Name: "docker", Version: "1", Reset: reset}))

	descriptor, err := registry.Require("docker", CapabilityReset)
	require.NoError(t, err)
	require.Equal(t, reset, descriptor.Reset)
	typed, err := registry.RequireReset("docker")
	require.NoError(t, err)
	require.Equal(t, reset, typed)
}

func (fakeGuestNetworkInit) ObserveGuestNetwork(context.Context, substrate.NodeHandle) (substrate.GuestNetworkInitObservation, error) {
	return substrate.GuestNetworkInitObservation{Mode: substrate.GuestNetworkInitCloudInit, Converged: true}, nil
}

func TestRegistryRequiresGuestNetworkInitCapability(t *testing.T) {
	registry := NewRegistry()
	guestInit := fakeGuestNetworkInit{}
	require.NoError(t, registry.Register(Descriptor{Name: "libvirt", Version: "1", GuestNetworkInit: guestInit}))

	descriptor, err := registry.Require("libvirt", CapabilityGuestNetworkInit)
	require.NoError(t, err)
	require.Equal(t, guestInit, descriptor.GuestNetworkInit)
	typed, err := registry.RequireGuestNetworkInit("libvirt")
	require.NoError(t, err)
	require.Equal(t, guestInit, typed)
	require.Equal(t, substrate.GuestNetworkInitMode("cloud_init"), substrate.GuestNetworkInitCloudInit)
	require.Equal(t, substrate.GuestNetworkInitMode("preconfigured"), substrate.GuestNetworkInitPreconfigured)
}

func TestDriverErrorsExposeStableCategories(t *testing.T) {
	err := Wrap(ErrorUnavailable, "docker", "daemon unavailable", errors.New("connection refused"))
	require.True(t, IsCategory(err, ErrorUnavailable))
	require.False(t, IsCategory(err, ErrorNotFound))
}
