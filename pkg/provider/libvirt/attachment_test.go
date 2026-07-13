package libvirt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestAttachmentPersistsStableMACInDomainState(t *testing.T) {
	handleState := &HandleState{}
	s := New()
	result, err := s.Attach(context.Background(), substrate.NodeHandle{ID: "vm", Provider: handleState}, driver.AttachmentRequest{Name: "internal", MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.20.0.10/24"}, Gateway: "10.20.0.1", NetworkState: json.RawMessage(`{"bridge":"br0"}`)})
	require.NoError(t, err)
	require.Equal(t, []BridgeAttach{{Name: "internal", Bridge: "br0", MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.20.0.10/24"}, Gateway: "10.20.0.1"}}, handleState.Bridges)
	require.Empty(t, result.GuestDevice)
	require.JSONEq(t, `{"bridge":"br0","mac":"02:00:00:00:00:01"}`, string(result.State))
}

func TestAttachmentUsesLibvirtRootBridge(t *testing.T) {
	handleState := &HandleState{}
	s := New()
	result, err := s.Attach(context.Background(), substrate.NodeHandle{ID: "vm", Provider: handleState}, driver.AttachmentRequest{
		Name: "matrix", MAC: "02:00:00:00:00:02", IPPrefixes: []string{"10.44.0.30/24"},
		NetworkState: json.RawMessage(`{"netns":"matrix-ns","bridge":"br-matrix","libvirt_bridge":"lv-matrix"}`),
	})

	require.NoError(t, err)
	require.Equal(t, "lv-matrix", handleState.Bridges[0].Bridge)
	require.JSONEq(t, `{"bridge":"lv-matrix","mac":"02:00:00:00:00:02"}`, string(result.State))
}

func TestBuildNoCloudNetworkConfigUsesMACAndStaticIPv4(t *testing.T) {
	data, err := buildNoCloudNetworkConfig([]BridgeAttach{{Name: "internal", MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.20.0.10/24"}, Gateway: "10.20.0.1"}})
	require.NoError(t, err)
	require.Contains(t, string(data), "version: 2")
	require.Contains(t, string(data), "02:00:00:00:00:01")
	require.Contains(t, string(data), "10.20.0.10/24")
	require.Contains(t, string(data), "to: 0.0.0.0/0")
	require.Contains(t, string(data), "via: 10.20.0.1")
}
