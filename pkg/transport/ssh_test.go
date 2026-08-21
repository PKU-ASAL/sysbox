package transport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecureSSHConnectionDoesNotDisableHostKeyVerification(t *testing.T) {
	args := NewSSHConnectionSecure("10.0.0.2", "22", "ubuntu", "", "secret").sshArgs()
	require.NotContains(t, args, "StrictHostKeyChecking=no")
	require.NotContains(t, args, "UserKnownHostsFile=/dev/null")
}

func TestSSHConnectionRunsSSHAndSCPInsideNetworkNamespace(t *testing.T) {
	connection := NewSSHConnectionInNamespace("sandbox-ns", "10.0.0.2", "22", "ubuntu", "/tmp/key", "")
	ssh := connection.command(context.Background(), []string{"ubuntu@10.0.0.2", "true"})
	require.Equal(t, []string{"ip", "netns", "exec", "sandbox-ns", resolveSSHBin(), "ubuntu@10.0.0.2", "true"}, ssh.Args)
	scp := connection.scpCommand(context.Background(), []string{"/tmp/source", "ubuntu@10.0.0.2:/tmp/target"})
	require.Equal(t, []string{"ip", "netns", "exec", "sandbox-ns", resolveSCPBin(), "/tmp/source", "ubuntu@10.0.0.2:/tmp/target"}, scp.Args)
}

func TestSSHConnectionUsesExplicitKnownHosts(t *testing.T) {
	args := NewSSHConnectionWithTrust("10.0.0.2", "22", "ubuntu", "", "", "/etc/sysbox/known_hosts", false).sshArgs()
	require.Contains(t, args, "StrictHostKeyChecking=yes")
	require.Contains(t, args, "UserKnownHostsFile=/etc/sysbox/known_hosts")
}

func TestSSHConnectionOnlyDisablesHostKeyCheckExplicitly(t *testing.T) {
	args := NewSSHConnectionWithTrust("10.0.0.2", "22", "ubuntu", "", "", "", true).sshArgs()
	require.Contains(t, args, "StrictHostKeyChecking=no")
}
