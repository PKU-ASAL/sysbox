package state

import (
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/stretchr/testify/require"
)

func TestStateResourceSeparatesAttributesAndDriverPrivateData(t *testing.T) {
	want := &State{Version: SchemaVersion, Resources: []Resource{{
		Address:    address.Resource("sysbox_node", "web"),
		Driver:     "docker",
		Attributes: map[string]any{"container_id": "abc"},
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
	var private DriverPrivate
	require.NoError(t, DecodePrivate(got.Resources[0].Private, 1, &private))
	require.Equal(t, "abc", private.Runtime["container_id"])
}
