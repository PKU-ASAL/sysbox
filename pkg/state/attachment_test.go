package state

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
)

func TestAttachmentRoundTripsDeterministically(t *testing.T) {
	node := address.Resource("sysbox_node", "web")
	in := &State{Version: SchemaVersion, Resources: []Resource{{
		Address: node,
		Attachments: []Attachment{{
			Name:       "uplink",
			Node:       node,
			Network:    address.Resource("sysbox_network", "public"),
			MAC:        "02:00:00:00:00:01",
			IPPrefixes: []string{"10.0.0.10/24"},
			Gateway:    "10.0.0.1",
			Driver:     "docker",
			Observation: AttachmentObservation{
				GuestDevice: "eth7",
			},
			DriverState: json.RawMessage(`{"network_id":"abc"}`),
		}},
	}}}

	first, err := in.Marshal()
	require.NoError(t, err)
	decoded, err := Unmarshal(first)
	require.NoError(t, err)
	second, err := decoded.Marshal()
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Len(t, decoded.Resources[0].Attachments, 1)
	got := decoded.Resources[0].Attachments[0]
	require.Equal(t, in.Resources[0].Attachments[0].Name, got.Name)
	require.Equal(t, in.Resources[0].Attachments[0].Node, got.Node)
	require.Equal(t, in.Resources[0].Attachments[0].Network, got.Network)
	require.Equal(t, in.Resources[0].Attachments[0].MAC, got.MAC)
	require.Equal(t, in.Resources[0].Attachments[0].IPPrefixes, got.IPPrefixes)
	require.Equal(t, in.Resources[0].Attachments[0].Gateway, got.Gateway)
	require.Equal(t, in.Resources[0].Attachments[0].Driver, got.Driver)
	require.Equal(t, in.Resources[0].Attachments[0].Observation, got.Observation)
	require.JSONEq(t, string(in.Resources[0].Attachments[0].DriverState), string(got.DriverState))
}
