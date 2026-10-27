//go:build e2e

package sshaccess

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestRealSSHDEffectiveConfigurationConflictE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sshd, err := exec.LookPath("sshd")
	require.NoError(t, err, "real OpenSSH server required")
	dir := t.TempDir()
	key := filepath.Join(dir, "host-key")
	out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput()
	require.NoError(t, err, string(out))
	current, err := user.Current()
	require.NoError(t, err)
	configs := []substrate.SSHAccessConfig{{BindIP: "127.0.0.1"}, {Port: 2222}}
	helper := "/usr/local/bin/sysbox-guest-task"
	generated, err := RenderSSHD(helper, current.Username, configs)
	require.NoError(t, err)
	base := fmt.Sprintf("HostKey %s\nUsePAM no\nAuthorizedKeysFile .ssh/authorized_keys\n", key)
	var probes []SSHDProbe
	for _, ip := range []string{"127.0.0.1", "127.0.0.2"} {
		for _, port := range []int{2222, 2223} {
			probes = append(probes, SSHDProbe{User: current.Username, RemoteAddress: "192.0.2.1", RemoteHost: "client.example", LocalAddress: ip, LocalPort: port, AuthorizedKeysFile: ".ssh/authorized_keys"})
		}
	}
	runner, err := NewSSHDRunner(sshd)
	require.NoError(t, err)
	for _, scenario := range []string{"valid", "earlier-command", "earlier-user", "earlier-key-file", "invalid-scope"} {
		t.Run(scenario, func(t *testing.T) {
			config := base
			switch scenario {
			case "earlier-command":
				config += "Match User " + current.Username + "\n  AuthorizedKeysCommand /usr/bin/false\n  AuthorizedKeysCommandUser " + current.Username + "\n"
			case "earlier-user":
				config += "Match User " + current.Username + "\n  AuthorizedKeysCommandUser nobody\n"
			case "earlier-key-file":
				config += "Match User " + current.Username + "\n  AuthorizedKeysFile none\n"
			}
			config += string(generated)
			if scenario == "invalid-scope" {
				config += "Match LocalPort 2222\nPort 2222\n"
			}
			path := filepath.Join(dir, scenario)
			require.NoError(t, os.WriteFile(path, []byte(config), 0600))
			err := ValidateSSHD(ctx, runner, path, helper, current.Username, configs, probes)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				if scenario != "invalid-scope" {
					require.True(t, strings.Contains(err.Error(), "configuration conflict"), err)
				}
			}
		})
	}
}
