package driver

import (
	"context"
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

func TestDriverErrorsExposeStableCategories(t *testing.T) {
	err := Wrap(ErrorUnavailable, "docker", "daemon unavailable", errors.New("connection refused"))
	require.True(t, IsCategory(err, ErrorUnavailable))
	require.False(t, IsCategory(err, ErrorNotFound))
}
