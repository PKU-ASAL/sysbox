package libvirt

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func decodeTestConfig(t *testing.T, source string) (*Config, error) {
	t.Helper()
	file, diags := hclsyntax.ParseConfig([]byte(source), "provider.hcl", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diags.HasErrors(), diags.Error())
	decoded, err := New().DecodeProviderConfig(file.Body, nil)
	if err != nil {
		return nil, err
	}
	return decoded.(*Config), nil
}

func TestDecodeProviderConfigRequiresExplicitNetworkInit(t *testing.T) {
	_, err := New().DecodeProviderConfig(nil, nil)
	require.ErrorContains(t, err, "network_init")
}

func TestDecodeProviderConfigRejectsUnknownNetworkInit(t *testing.T) {
	_, err := decodeTestConfig(t, `network_init = "automatic"`)
	require.ErrorContains(t, err, "network_init")
}

func TestDecodeProviderConfigAcceptsSupportedNetworkInitModes(t *testing.T) {
	for _, mode := range []substrate.GuestNetworkInitMode{substrate.GuestNetworkInitCloudInit, substrate.GuestNetworkInitPreconfigured} {
		cfg, err := decodeTestConfig(t, `network_init = "`+string(mode)+`"`)
		require.NoError(t, err)
		require.Equal(t, mode, cfg.NetworkInit)
	}
	require.ElementsMatch(t, []substrate.GuestNetworkInitMode{substrate.GuestNetworkInitCloudInit, substrate.GuestNetworkInitPreconfigured}, New().Capabilities().GuestNetworkInitModes)
}
