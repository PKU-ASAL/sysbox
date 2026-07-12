package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
)

func TestDeterministicMACIsStableLocalUnicast(t *testing.T) {
	owner := address.Resource("sysbox_node", "web")
	first := DeterministicMAC("lab", owner, "uplink")
	second := DeterministicMAC("lab", owner, "uplink")

	require.Equal(t, first, second)
	require.Zero(t, first[0]&1)
	require.NotZero(t, first[0]&2)
	require.NotEqual(t, first, DeterministicMAC("lab", owner, "internal"))
}

func TestNormalizeAttachmentIntentsUsesExplicitMACAndCanonicalPrefix(t *testing.T) {
	owner := address.Resource("sysbox_node", "web")
	intents, err := NormalizeAttachmentIntents("lab", owner, []AttachmentInput{{
		Name:       "uplink",
		Network:    "sysbox_network.public",
		IPPrefixes: []string{"10.0.0.10/24"},
		Gateway:    "10.0.0.1",
		MAC:        "02:00:00:00:00:0A",
	}})

	require.NoError(t, err)
	require.Equal(t, []AttachmentIntent{{
		Name:       "uplink",
		Network:    address.Resource("sysbox_network", "public"),
		MAC:        "02:00:00:00:00:0a",
		IPPrefixes: []string{"10.0.0.10/24"},
		Gateway:    "10.0.0.1",
	}}, intents)
}

func TestNormalizeAttachmentIntentsRejectsInvalidInput(t *testing.T) {
	owner := address.Resource("sysbox_node", "web")
	tests := []struct {
		name   string
		inputs []AttachmentInput
		want   string
	}{
		{name: "duplicate name", inputs: []AttachmentInput{{Name: "uplink", Network: "sysbox_network.public"}, {Name: "uplink", Network: "sysbox_network.backup"}}, want: `duplicate attachment name "uplink"`},
		{name: "duplicate prefix", inputs: []AttachmentInput{{Name: "uplink", Network: "sysbox_network.public", IPPrefixes: []string{"10.0.0.10/24", "10.0.0.10/24"}}}, want: `duplicate IP prefix "10.0.0.10/24"`},
		{name: "multicast mac", inputs: []AttachmentInput{{Name: "uplink", Network: "sysbox_network.public", MAC: "01:00:5e:00:00:01"}}, want: "unicast"},
		{name: "invalid prefix", inputs: []AttachmentInput{{Name: "uplink", Network: "sysbox_network.public", IPPrefixes: []string{"bad"}}}, want: "invalid IP prefix"},
		{name: "gateway outside prefix", inputs: []AttachmentInput{{Name: "uplink", Network: "sysbox_network.public", IPPrefixes: []string{"10.0.0.10/24"}, Gateway: "10.1.0.1"}}, want: "gateway 10.1.0.1 is outside"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeAttachmentIntents("lab", owner, tt.inputs)
			require.ErrorContains(t, err, tt.want)
			require.ErrorContains(t, err, "sysbox_node.web")
		})
	}
}
