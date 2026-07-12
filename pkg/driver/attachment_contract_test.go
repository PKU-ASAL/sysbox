package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/substrate"
)

type attachmentLifecycleStub struct{}

func (attachmentLifecycleStub) Attach(context.Context, substrate.NodeHandle, AttachmentRequest) (AttachmentResult, error) {
	return AttachmentResult{Driver: "stub", GuestDevice: "net0", State: json.RawMessage(`{"id":"opaque"}`)}, nil
}
func (attachmentLifecycleStub) Observe(context.Context, substrate.NodeHandle, AttachmentRequest, json.RawMessage) (AttachmentResult, error) {
	return AttachmentResult{Driver: "stub", GuestDevice: "net0"}, nil
}
func (attachmentLifecycleStub) Delete(context.Context, substrate.NodeHandle, AttachmentRequest, json.RawMessage) error {
	return nil
}

func TestNICCapabilityDeclaresAttachmentLifecycle(t *testing.T) {
	var nic NIC = attachmentLifecycleStub{}
	request := AttachmentRequest{Name: "uplink", Network: address.Resource("sysbox_network", "public"), MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.0.0.10/24"}}
	result, err := nic.Attach(context.Background(), substrate.NodeHandle{ID: "node"}, request)
	require.NoError(t, err)
	require.Equal(t, "net0", result.GuestDevice)
	require.JSONEq(t, `{"id":"opaque"}`, string(result.State))
}
