package state

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
)

func TestStateV4RoundTripPreservesTypedResource(t *testing.T) {
	attributes, err := NewAttributes(map[string]any{"primary_ip": "10.0.0.2", "vcpus": 2})
	require.NoError(t, err)
	private, err := EncodePrivate(1, map[string]any{"container_id": "abc", "pid": 42})
	require.NoError(t, err)
	want := &State{Version: SchemaVersion, Lineage: "lineage-1", Resources: []Resource{{
		Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker", SchemaVersion: 1,
		ExternalID: "abc", Attributes: attributes, Private: private, Dependencies: []address.Address{address.Resource("sysbox_network", "lab")}, Status: ResourcePresent,
	}}}

	raw, err := want.Marshal()
	require.NoError(t, err)
	got, err := Unmarshal(raw)
	require.NoError(t, err)
	require.Equal(t, "lineage-1", got.Lineage)
	require.Equal(t, attributes.GoValue(), got.Resources[0].Attributes.GoValue())
	require.Equal(t, "abc", got.Resources[0].ExternalID)
	var payload map[string]any
	require.NoError(t, DecodePrivate(got.Resources[0].Private, 1, &payload))
	require.Equal(t, "abc", payload["container_id"])
}

func TestStateV4RejectsV3WithoutMutation(t *testing.T) {
	raw := []byte(`{"version":3,"resources":[]}`)
	before := append([]byte(nil), raw...)
	_, err := Unmarshal(raw)
	require.Error(t, err)
	require.Equal(t, before, raw)
}

func TestPrivateEnvelopeRejectsWrongVersion(t *testing.T) {
	raw, err := json.Marshal(PrivateEnvelope{Version: 2, Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	var target map[string]any
	require.ErrorContains(t, DecodePrivate(raw, 1, &target), "private state version")
}
