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

func TestWindowsETWBootstrapIsOptIn(t *testing.T) {
	defaultConfig, err := decodeTestConfig(t, `network_init = "preconfigured"`)
	require.NoError(t, err)
	require.Empty(t, defaultConfig.BootstrapProvisioners())
	require.Empty(t, defaultConfig.ObservationSource())

	config, err := decodeTestConfig(t, `network_init = "preconfigured"
windows_etw = true`)
	require.NoError(t, err)
	require.Equal(t, "etw", config.ObservationSource())
	steps := config.BootstrapProvisioners()
	require.Len(t, steps, 4)
	require.Equal(t, "exec", steps[0].Type)
	require.Equal(t, "powershell", steps[0].Shell)
	require.Equal(t, "file", steps[1].Type)
	require.Contains(t, steps[1].Source, "/windows-etw/sysarmor-windows-etw.zip")
	require.Equal(t, "powershell", steps[3].Shell)
}
