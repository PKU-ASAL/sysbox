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
	result, err := s.Attach(context.Background(), substrate.NodeHandle{ID: "vm", Provider: handleState}, driver.AttachmentRequest{Name: "internal", MAC: "02:00:00:00:00:01", NetworkState: json.RawMessage(`{"bridge":"br0"}`)})
	require.NoError(t, err)
	require.Equal(t, []BridgeAttach{{Bridge: "br0", MAC: "02:00:00:00:00:01"}}, handleState.Bridges)
	require.Empty(t, result.GuestDevice)
	require.JSONEq(t, `{"bridge":"br0","mac":"02:00:00:00:00:01"}`, string(result.State))
}
