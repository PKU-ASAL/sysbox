package runtime

import (
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAdoptStateResourceRewritesExternalIDs(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	AdoptStateResource(st, StateResourceLog{
		Type: "sysbox_node", Name: "web",
		Provider: "docker",
		Instance: map[string]any{
			"container_id": "old",
			"primary_ip":   "10.0.0.2",
		},
	}, "new-container")

	res := st.FindResource(address.Resource("sysbox_node", "web"))
	require.NotNil(t, res)
	require.Equal(t, "new-container", res.ContainerID())
	require.Equal(t, "10.0.0.2", res.PrimaryIP())

	AdoptStateResource(st, StateResourceLog{
		Type: "sysbox_network", Name: "egress",
		Provider: "docker",
		Instance: map[string]any{
			"docker_network_id": "old-net",
			"nat":               true,
		},
	}, "new-net")

	net := st.FindResource(address.Resource("sysbox_network", "egress"))
	require.NotNil(t, net)
	require.Equal(t, "new-net", net.DockerNetID())
	require.True(t, net.IsNAT())
}
