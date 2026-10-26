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

func TestManagedBridgePrivateIdentitySurvivesUnrelatedWrites(t *testing.T) {
	raw := json.RawMessage("{\"inode\":9007199254740993,\"device\":18446744073709551615,\"identity\":{\"generation\":9007199254740993}}")
	r := Resource{}
	require.NoError(t, r.SetRuntimeValue("managed_bridge", raw))
	require.NoError(t, r.SetRuntimeValue("pid", 42))
	require.NoError(t, r.SetProviderState([]byte("{\"other\":true}")))
	require.NoError(t, r.DeleteRuntimeValue("pid"))
	encoded, err := json.Marshal(r.RuntimeValue("managed_bridge"))
	require.NoError(t, err)
	require.Equal(t, string(raw), string(encoded))
}
