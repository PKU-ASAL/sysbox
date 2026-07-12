package state

import (
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/stretchr/testify/require"
)

func TestStateResourceIdentityUsesCanonicalAddress(t *testing.T) {
	blue := address.StringInstance("sysbox_node", "web", "blue")
	green := address.StringInstance("sysbox_node", "web", "green")
	st := &State{Version: SchemaVersion, Resources: []Resource{
		{Address: blue, Provider: "docker", Instance: map[string]any{"id": "blue-id"}},
		{Address: green, Provider: "docker", Instance: map[string]any{"id": "green-id"}},
	}}

	require.Equal(t, "blue-id", st.FindResource(blue).Str("id"))
	require.Equal(t, "green-id", st.FindResource(green).Str("id"))
	st.RemoveResource(blue)
	require.Nil(t, st.FindResource(blue))
	require.NotNil(t, st.FindResource(green))
}

func TestStateAddressJSONRoundTrip(t *testing.T) {
	want := &State{Version: SchemaVersion, Resources: []Resource{{
		Address: address.StringInstance("sysbox_node", "web", "blue"),
	}}}
	raw, err := want.Marshal()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"address": "sysbox_node.web[\"blue\"]"`)
	require.NotContains(t, string(raw), `"type"`)
	require.NotContains(t, string(raw), `"name"`)

	got, err := Unmarshal(raw)
	require.NoError(t, err)
	require.Equal(t, want.Resources[0].Address.String(), got.Resources[0].Address.String())
}

func TestAddResourceOwnsAddress(t *testing.T) {
	addr := address.Resource("sysbox_node", "web").WithModule(address.ModuleInstance{Name: "lab"})
	st := &State{}
	st.AddResource(Resource{Address: addr})

	addr.ModulePath[0].Name = "mutated"
	require.Equal(t, "module.lab.sysbox_node.web", st.Resources[0].Address.String())
}
