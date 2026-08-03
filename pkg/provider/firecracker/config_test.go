package firecracker

import (
	"encoding/json"
	"testing"

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

type nilStateReader struct{}

func (nilStateReader) ResourceInstance(address.Address) map[string]any { return nil }
