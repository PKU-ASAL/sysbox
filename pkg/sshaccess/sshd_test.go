package sshaccess

import (
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestDropInIncludesEndpointIntersectionsBeforeWildcards(t *testing.T) {
	configs := []substrate.SSHAccessConfig{{BindIP: "127.0.0.1"}, {Port: 2222}, {Port: 22}, {BindIP: "::1"}, {Port: 2222}}
	text, err := RenderSSHD("/usr/local/bin/sysbox-guest-task", "root", configs)
	require.NoError(t, err)
	require.Contains(t, string(text), "Match LocalAddress 127.0.0.1 LocalPort 2222\n")
	require.Contains(t, string(text), "--ssh-authorized-keys %u 127.0.0.1 2222\n")
	require.Contains(t, string(text), "Match LocalAddress 127.0.0.1\n")
	require.Contains(t, string(text), "--ssh-authorized-keys %u 127.0.0.1 0\n")
	require.Contains(t, string(text), "Match LocalPort 2222\n")
	require.Contains(t, string(text), "--ssh-authorized-keys %u - 2222\n")
	require.Contains(t, string(text), "Match all\n  AuthorizedKeysCommand /usr/local/bin/sysbox-guest-task --ssh-authorized-keys %u\n")
	require.NotContains(t, string(text), "AuthorizedKeysFile")
	require.NotContains(t, string(text), "ListenAddress")
	exact := strings.Index(string(text), "Match LocalAddress 127.0.0.1 LocalPort 2222\n")
	addressOnly := strings.Index(string(text), "Match LocalAddress 127.0.0.1\n")
	portOnly := strings.Index(string(text), "Match LocalPort 2222\n")
	all := strings.Index(string(text), "Match all\n")
	require.Less(t, exact, addressOnly)
	require.Less(t, exact, portOnly)
	require.Less(t, addressOnly, all)
	require.Less(t, portOnly, all)
	reverse := []substrate.SSHAccessConfig{configs[4], configs[3], configs[2], configs[1], configs[0]}
	again, err := RenderSSHD("/usr/local/bin/sysbox-guest-task", "root", reverse)
	require.NoError(t, err)
	require.Equal(t, text, again)
}

func TestDropInRejectsUnsafeInputsAndUnboundedExpansion(t *testing.T) {
	for _, helper := range []string{"relative", "/bin/helper %u", "/bin/helper\nPort 1", "/bin/%u", "/bin/../helper"} {
		_, err := RenderSSHD(helper, "root", nil)
		require.Error(t, err)
	}
	for _, configs := range [][]substrate.SSHAccessConfig{{{BindIP: "x\nMatch all"}}, {{Port: -1}}, {{Port: 65536}}} {
		_, err := RenderSSHD("/bin/helper", "root", configs)
		require.Error(t, err)
	}
	_, err := RenderSSHD("/bin/helper", "root\nPort 1", nil)
	require.Error(t, err)
	configs := make([]substrate.SSHAccessConfig, 129)
	for i := range configs {
		configs[i].Port = i + 1
	}
	_, err = RenderSSHD("/bin/helper", "root", configs)
	require.Error(t, err)
}
