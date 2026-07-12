package state

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type testNodeStateCodec struct{}

func (testNodeStateCodec) UnmarshalProviderState(data json.RawMessage) (any, error) {
	var decoded map[string]any
	return decoded, json.Unmarshal(data, &decoded)
}

func TestReconstructHandleRequiresOnlyNodeStateCodec(t *testing.T) {
	resource := Resource{Attributes: MustAttributes(map[string]any{
		"container_id": "node-1", "primary_ip": "10.0.0.2",
	})}
	require.NoError(t, resource.SetProviderState(json.RawMessage(`{"pid":42}`)))

	handle, err := resource.ReconstructHandle(testNodeStateCodec{})
	require.NoError(t, err)
	require.Equal(t, "node-1", handle.ID)
	require.Equal(t, "10.0.0.2", handle.Net.PrimaryIP)
	require.Equal(t, float64(42), handle.Provider.(map[string]any)["pid"])
}
