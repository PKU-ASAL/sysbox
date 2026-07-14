package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestResolveGuestFamilyStrictRules(t *testing.T) {
	tests := []struct {
		name     string
		image    substrate.GuestFamily
		override substrate.GuestFamily
		want     substrate.GuestFamily
		wantErr  string
	}{
		{name: "inherit known", image: substrate.GuestFamilyLinux, want: substrate.GuestFamilyLinux},
		{name: "matching override", image: substrate.GuestFamilyWindows, override: substrate.GuestFamilyWindows, want: substrate.GuestFamilyWindows},
		{name: "resolve unknown", image: substrate.GuestFamilyUnknown, override: substrate.GuestFamilyLinux, want: substrate.GuestFamilyLinux},
		{name: "remain unknown", image: substrate.GuestFamilyUnknown, want: substrate.GuestFamilyUnknown},
		{name: "conflict", image: substrate.GuestFamilyLinux, override: substrate.GuestFamilyWindows, wantErr: "conflicts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveGuestFamily(tt.image, tt.override)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestComputePlanRejectsGuestFamilyConflictBeforeProviderLookup(t *testing.T) {
	g := graph.New()
	imageAddr := address.Resource("sysbox_image", "windows")
	nodeAddr := address.Resource("sysbox_node", "server")
	require.NoError(t, g.AddNode(imageAddr, nil))
	require.NoError(t, g.SetData(imageAddr, &config.ImageConfig{Substrate: "missing", Kind: "qcow2", Source: "/images/windows.qcow2", Architecture: "amd64", GuestFamily: "windows"}))
	require.NoError(t, g.AddNode(nodeAddr, []address.Address{imageAddr}))
	require.NoError(t, g.SetData(nodeAddr, &config.NodeConfig{Substrate: "missing", Image: "sysbox_image.windows.id", GuestFamily: "linux"}))

	_, err := ComputePlan(g, &state.State{Version: state.SchemaVersion})
	require.ErrorContains(t, err, "conflicts")
}

func TestComputePlanRejectsUnknownFamilyProvisioning(t *testing.T) {
	g := graph.New()
	imageAddr := address.Resource("sysbox_image", "unknown")
	nodeAddr := address.Resource("sysbox_node", "server")
	require.NoError(t, g.AddNode(imageAddr, nil))
	require.NoError(t, g.SetData(imageAddr, &config.ImageConfig{Substrate: "missing", Kind: "raw", Source: "/images/guest.raw", Architecture: "amd64", GuestFamily: "unknown"}))
	require.NoError(t, g.AddNode(nodeAddr, []address.Address{imageAddr}))
	require.NoError(t, g.SetData(nodeAddr, &config.NodeConfig{
		Substrate: "missing", Image: "sysbox_image.unknown.id",
		Provisioners: []config.ProvisionerConfig{{Type: "exec", Inline: []string{"true"}}},
	}))

	_, err := ComputePlan(g, &state.State{Version: state.SchemaVersion})
	require.ErrorContains(t, err, "unknown guest family")
}
