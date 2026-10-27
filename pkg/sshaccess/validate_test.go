package sshaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestValidateEffectiveSSHCommandAndUnmanagedKeys(t *testing.T) {
	probe := SSHDProbe{User: "ubuntu", RemoteAddress: "192.0.2.10", RemoteHost: "client.example", LocalAddress: "127.0.0.1", LocalPort: 2222, AuthorizedKeysFile: ".ssh/authorized_keys"}
	configs := []substrate.SSHAccessConfig{{BindIP: "127.0.0.1", Port: 2222}}
	for _, scenario := range []string{"valid", "foreign-command", "wrong-user", "key-file-changed", "syntax", "probe-error", "missing", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			run := func(_ context.Context, args ...string) ([]byte, error) {
				calls++
				if calls == 1 {
					require.Equal(t, []string{"-t", "-f", "/etc/ssh/candidate"}, args)
					if scenario == "syntax" {
						return nil, errors.New("invalid config")
					}
					return nil, nil
				}
				require.Equal(t, []string{"-T", "-f", "/etc/ssh/candidate", "-C", "user=ubuntu,host=client.example,addr=192.0.2.10,laddr=127.0.0.1,lport=2222"}, args)
				if scenario == "probe-error" {
					return nil, errors.New("probe failed")
				}
				command, user, keyfile := "/usr/local/bin/sysbox-guest-task --ssh-authorized-keys %u 127.0.0.1 2222", "root", ".ssh/authorized_keys"
				if scenario == "foreign-command" {
					command = "/usr/bin/third-party"
				}
				if scenario == "wrong-user" {
					user = "nobody"
				}
				if scenario == "key-file-changed" {
					keyfile = "none"
				}
				if scenario == "missing" {
					return []byte("port 2222\n"), nil
				}
				output := "authorizedkeyscommand " + command + "\nauthorizedkeyscommanduser " + user + "\nauthorizedkeysfile " + keyfile + "\n"
				if scenario == "duplicate" {
					output += "authorizedkeyscommand /usr/bin/other\n"
				}
				return []byte(output), nil
			}
			err := ValidateSSHD(context.Background(), run, "/etc/ssh/candidate", "/usr/local/bin/sysbox-guest-task", "root", configs, []SSHDProbe{probe})
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if scenario == "syntax" {
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestValidateSSHRejectsMissingOrInjectedProbeBeforeExecuting(t *testing.T) {
	good := SSHDProbe{User: "ubuntu", RemoteAddress: "192.0.2.1", RemoteHost: "client", LocalAddress: "127.0.0.1", LocalPort: 22, AuthorizedKeysFile: ".ssh/authorized_keys"}
	bad := good
	bad.RemoteHost = "client,user=root"
	for _, probes := range [][]SSHDProbe{nil, {bad}, {{}}} {
		err := ValidateSSHD(context.Background(), func(context.Context, ...string) ([]byte, error) { t.Fatal("invalid probe executed"); return nil, nil }, "/etc/ssh/candidate", "/bin/helper", "root", nil, probes)
		require.Error(t, err)
	}
}
