package api

import (
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestAdoptStateResourceDropsLegacyProviderExtra(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	runtime.AdoptStateResource(st, runtime.StateResourceLog{
		Type: "sysbox_node", Name: "vm",
		Provider: "firecracker",
		Instance: map[string]any{
			"container_id":    "sysbox-vm",
			"provider_extra":  `{"vm_dir":"/tmp/sysbox-vm"}`,
			"desired_hash_v2": "hash",
		},
	}, "")

	res := st.FindResource(address.Resource("sysbox_node", "vm"))
	require.NotNil(t, res)
	require.Equal(t, "sysbox-vm", res.ContainerID())
	providerState, err := res.ProviderState()
	require.NoError(t, err)
	require.Empty(t, providerState)
	require.NotContains(t, res.Attributes, "provider_extra")
}
