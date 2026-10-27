package libvirt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
)

func TestAttachmentPersistsStableMACInDomainState(t *testing.T) {
	id := managedOperation()
	handleState := &HandleState{Identity: &id, DomainUUID: "vm"}
	s := New()
	result, err := s.Attach(context.Background(), managedNodeHandle(handleState), driver.AttachmentRequest{NetworkIdentity: id, Name: "internal", MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.20.0.10/24"}, Gateway: "10.20.0.1", NetworkState: json.RawMessage(`{"netns":"matrix-ns","libvirt_bridge":"br0"}`)})
	require.NoError(t, err)
	require.Equal(t, []BridgeAttach{{Target: network.BridgeTarget{Identity: id, NetworkNamespace: "matrix-ns", Bridge: "br0", Root: true}, Name: "internal", Netns: "matrix-ns", Bridge: "br0", MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.20.0.10/24"}, Gateway: "10.20.0.1"}}, handleState.Bridges)
	require.Empty(t, result.GuestDevice)
	require.JSONEq(t, `{"bridge":"br0","mac":"02:00:00:00:00:01"}`, string(result.State))
}

func TestAttachmentUsesLibvirtRootBridge(t *testing.T) {
	id := managedOperation()
	handleState := &HandleState{Identity: &id, DomainUUID: "vm"}
	s := New()
	result, err := s.Attach(context.Background(), managedNodeHandle(handleState), driver.AttachmentRequest{
		NetworkIdentity: id,
		Name:            "matrix", MAC: "02:00:00:00:00:02", IPPrefixes: []string{"10.44.0.30/24"},
		NetworkState: json.RawMessage(`{"netns":"matrix-ns","bridge":"br-matrix","libvirt_bridge":"lv-matrix"}`),
	})

	require.NoError(t, err)
	require.Equal(t, "lv-matrix", handleState.Bridges[0].Bridge)
	require.JSONEq(t, `{"bridge":"lv-matrix","mac":"02:00:00:00:00:02"}`, string(result.State))
}

func TestDomainXMLHasInterfaceDetectsExternalNICDrift(t *testing.T) {
	raw := []byte(`<domain><devices><interface type="bridge"><source bridge="br0"/><mac address="02:00:00:00:00:01"/></interface></devices></domain>`)
	require.True(t, domainXMLHasInterface(raw, "br0", "02:00:00:00:00:01"))
	require.False(t, domainXMLHasInterface(raw, "br1", "02:00:00:00:00:01"))
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

func TestAttachmentRetryDoesNotDuplicateOrReplaceLogicalInterface(t *testing.T) {
	id := managedOperation()
	hs := &HandleState{Identity: &id, DomainUUID: "vm"}
	h := managedNodeHandle(hs)
	s := New()
	req := driver.AttachmentRequest{NetworkIdentity: id, Name: "lan", MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.20.0.2/24"}, Gateway: "10.20.0.1", NetworkState: json.RawMessage("{\"netns\":\"owned\",\"libvirt_bridge\":\"br0\"}")}
	original, err := s.Attach(context.Background(), h, req)
	require.NoError(t, err)
	again, err := s.Attach(context.Background(), h, req)
	require.NoError(t, err)
	require.Equal(t, original, again)
	require.Len(t, hs.Bridges, 1, "same logical attachment must not emit duplicate NICs on restart")
	for _, change := range []func(*driver.AttachmentRequest){
		func(r *driver.AttachmentRequest) { r.MAC = "02:00:00:00:00:02" },
		func(r *driver.AttachmentRequest) { r.NetworkIdentity.Generation++ },
		func(r *driver.AttachmentRequest) { r.IPPrefixes = []string{"10.20.0.3/24"} },
		func(r *driver.AttachmentRequest) { r.Gateway = "10.20.0.254" },
		func(r *driver.AttachmentRequest) {
			r.NetworkState = json.RawMessage("{\"netns\":\"replacement\",\"libvirt_bridge\":\"br1\"}")
		},
	} {
		altered := req
		change(&altered)
		_, err = s.Attach(context.Background(), h, altered)
		require.Error(t, err, "same logical interface cannot silently change identity/configuration")
		require.Len(t, hs.Bridges, 1)
	}
}

func TestAttachmentColdRetryAndDuplicateRecordRefusal(t *testing.T) {
	id := managedOperation()
	hs := &HandleState{Identity: &id, DomainUUID: "vm"}
	req := driver.AttachmentRequest{NetworkIdentity: id, Name: "lan", MAC: "02:00:00:00:00:01", NetworkState: json.RawMessage("{\"netns\":\"owned\",\"libvirt_bridge\":\"br0\"}")}
	original, err := New().Attach(context.Background(), managedNodeHandle(hs), req)
	require.NoError(t, err)
	raw, err := json.Marshal(hs)
	require.NoError(t, err)
	var cold HandleState
	require.NoError(t, json.Unmarshal(raw, &cold))
	again, err := New().Attach(context.Background(), managedNodeHandle(&cold), req)
	require.NoError(t, err)
	require.Equal(t, original, again)
	require.Len(t, cold.Bridges, 1)
	cold.Bridges = append(cold.Bridges, cold.Bridges[0])
	before, err := json.Marshal(cold)
	require.NoError(t, err)
	_, err = New().Attach(context.Background(), managedNodeHandle(&cold), req)
	require.Error(t, err)
	after, err := json.Marshal(cold)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
