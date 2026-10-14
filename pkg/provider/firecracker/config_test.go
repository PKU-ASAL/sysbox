package firecracker

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestParseMemoryRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"abc", "0", "-1", "512.5"} {
		_, err := parseMemoryMiB(value)
		require.Error(t, err, value)
	}
	got, err := parseMemoryMiB("2048MiB")
	require.NoError(t, err)
	require.Equal(t, 2048, got)
}

func TestPrepareHandlePersistsSSHCredentials(t *testing.T) {
	s := New("/tmp/kernel", t.TempDir())
	h := substrate.NodeHandle{ID: "vm", Provider: &HandleState{}, Net: substrate.NetInfo{PrimaryIP: "10.0.0.2"}}
	require.NoError(t, s.PrepareHandle(nil, &h, &Config{SSHUser: "ubuntu", SSHPass: "secret", SSHPort: 2222}, nilStateReader{}))
	hs := h.Provider.(*HandleState)
	require.Equal(t, "ubuntu", hs.SSHUser)
	require.Equal(t, "secret", hs.SSHPass)
	require.Equal(t, "2222", hs.SSHPort)
	raw, err := json.Marshal(hs)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
}

func TestValidateRequiresExplicitDirectModeOptIn(t *testing.T) {
	s := New("/tmp/kernel", t.TempDir())
	err := s.Validate(substrate.NodeSpec{ProviderConfig: &Config{}})
	require.ErrorContains(t, err, "allow_direct")
	require.NoError(t, s.Validate(substrate.NodeSpec{ProviderConfig: &Config{AllowDirect: true}}))
}

func TestDecodeSysArmorProviderOption(t *testing.T) {
	parsed, diags := hclsyntax.ParseConfig([]byte("sysarmor = true\nallow_direct = true\n"), "vm.hcl", hcl.InitialPos)
	require.False(t, diags.HasErrors(), diags.Error())
	decoded, err := New("/tmp/kernel", t.TempDir()).DecodeProviderConfig(parsed.Body, nil)
	require.NoError(t, err)
	require.True(t, decoded.(*Config).SysArmor)
}

func TestDisabledSysArmorDoesNotChangeDefaultConfigEncoding(t *testing.T) {
	disabled, err := json.Marshal(&Config{})
	require.NoError(t, err)
	require.NotContains(t, string(disabled), "SysArmor")
	enabled, err := json.Marshal(&Config{SysArmor: true})
	require.NoError(t, err)
	require.Contains(t, string(enabled), `"SysArmor":true`)
}

func TestSysArmorBootstrapProvisioners(t *testing.T) {
	require.Empty(t, (&Config{}).BootstrapProvisioners())
	provisioners := (&Config{SysArmor: true}).BootstrapProvisioners()
	require.Len(t, provisioners, 4)
	require.Equal(t, "/var/lib/sysbox/vm-artifacts/sysarmor/sysarmor-agent-deploy-76263fca.tar.gz", provisioners[0].Source)
	require.Equal(t, "/tmp/sysarmor-agent-deploy.tar.gz", provisioners[0].Destination)
	require.Equal(t, []string{"/tmp/install-sysarmor-vm.sh", "/tmp/sysarmor-agent-deploy.tar.gz", "/tmp/sysarmor-standalone-vm.yaml"}, provisioners[3].Args)
}

type nilStateReader struct{}

func (nilStateReader) ResourceInstance(address.Address) map[string]any { return nil }
