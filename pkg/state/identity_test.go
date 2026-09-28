package state

import (
	"github.com/oslab/sysbox/pkg/address"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResourceIdentitySurvivesStateRoundTrip(t *testing.T) {
	st := &State{Version: SchemaVersion, TopologyUID: "topology-uid"}
	st.AddResource(Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: "owner", ResourceUID: "resource", Generation: 3, ExternalID: "node"})
	raw, err := st.Marshal()
	require.NoError(t, err)
	restored, err := Unmarshal(raw)
	require.NoError(t, err)
	require.Equal(t, st.TopologyUID, restored.TopologyUID)
	r := restored.Resources[0]
	require.Equal(t, int64(3), r.Generation)
	require.Equal(t, "resource", r.ResourceUID)
	h, err := r.ReconstructHandle(nil)
	require.NoError(t, err)
	require.Equal(t, int64(3), h.Generation)
}
