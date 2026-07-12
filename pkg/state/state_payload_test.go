package state

import (
	"encoding/json"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/stretchr/testify/require"
)

func TestStateResourceSeparatesAttributesAndDriverPrivateData(t *testing.T) {
	want := &State{Version: SchemaVersion, Resources: []Resource{{
		Address:    address.Resource("sysbox_node", "web"),
		Driver:     "docker",
		Attributes: map[string]any{"container_id": "abc"},
		Private:    json.RawMessage(`{"runtime":"opaque"}`),
	}}}

	raw, err := want.Marshal()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"driver": "docker"`)
	require.Contains(t, string(raw), `"attributes"`)
	require.Contains(t, string(raw), `"private"`)
	require.NotContains(t, string(raw), `"provider"`)
	require.NotContains(t, string(raw), `"instance"`)

	got, err := Unmarshal(raw)
	require.NoError(t, err)
	require.Equal(t, "docker", got.Resources[0].Driver)
	require.Equal(t, "abc", got.Resources[0].Str("container_id"))
	require.JSONEq(t, `{"runtime":"opaque"}`, string(got.Resources[0].Private))
}
