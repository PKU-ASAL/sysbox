//go:build e2e

package sshaccess

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRealSSHDDeploymentReloadRecoveryE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	daemon, err := exec.LookPath("sshd")
	require.NoError(t, err)
	current, err := user.Current()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	for _, name := range []string{"host", "administrator"} {
		out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, name)).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	main, drop := filepath.Join(dir, "sshd_config"), filepath.Join(dir, "sysbox.conf")
	config := fmt.Sprintf("ListenAddress 127.0.0.1\nPort %d\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nUsePAM no\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin prohibit-password\nAllowUsers %s\nInclude %s\n", port, filepath.Join(dir, "host"), filepath.Join(dir, "pid"), filepath.Join(dir, "administrator.pub"), current.Username, drop)
	require.NoError(t, os.WriteFile(main, []byte(config), 0600))
	log, err := os.Create(filepath.Join(dir, "daemon.log"))
	require.NoError(t, err)
	defer log.Close()
	command := exec.CommandContext(ctx, daemon, "-D", "-e", "-f", main)
	command.Stdout = log
	command.Stderr = log
	require.NoError(t, command.Start())
	defer func() { command.Process.Kill(); command.Wait() }()
	endpoint := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	require.Eventually(t, func() bool { return sshListenerReady(ctx, endpoint) == nil }, 3*time.Second, 20*time.Millisecond)
	private, err := os.ReadFile(filepath.Join(dir, "administrator"))
	require.NoError(t, err)
	signer, err := ssh.ParsePrivateKey(private)
	require.NoError(t, err)
	hostPublic, err := os.ReadFile(filepath.Join(dir, "host.pub"))
	require.NoError(t, err)
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostPublic)
	require.NoError(t, err)
	login := func() error {
		client, err := ssh.Dial("tcp", endpoint, &ssh.ClientConfig{User: current.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: time.Second})
		if err == nil {
			client.Close()
		}
		return err
	}
	require.NoError(t, login(), "administrator login before deployment")
	processControl, err := processController(ServiceProfile{PIDFile: filepath.Join(dir, "pid"), SSHD: daemon, ConfigPath: main})
	require.NoError(t, err)
	require.NoError(t, processControl(ctx, "is-active"))
	runner, err := NewSSHDRunner(daemon)
	require.NoError(t, err)
	d := &Deployment{ServiceIdentity: main, Root: filepath.Join(dir, "journal"), ConfigPath: main, DropInPath: drop, Helper: "/usr/bin/true", CommandUser: current.Username, Run: runner,
		Probes: []SSHDProbe{{User: current.Username, RemoteHost: "localhost", RemoteAddress: "127.0.0.1", LocalAddress: "127.0.0.1", LocalPort: port, AuthorizedKeysFile: filepath.Join(dir, "administrator.pub")}},
		Reload: func(ctx context.Context) error { return processControl(ctx, "reload") }, Ready: func(ctx context.Context) error { return sshListenerReady(ctx, endpoint) },
	}
	require.NoError(t, d.Ensure(ctx, []substrate.SSHAccessConfig{{Port: port}}))
	require.Eventually(t, func() bool { return login() == nil }, 3*time.Second, 20*time.Millisecond, "administrator login preserved after reload")
	cold := *d
	require.NoError(t, cold.Recover(ctx))
	require.Eventually(t, func() bool { return login() == nil }, 3*time.Second, 20*time.Millisecond)
	raw, err := os.ReadFile(main)
	require.NoError(t, err)
	require.Equal(t, config, string(raw))
	// A trusted but substituted PID file must never select an unrelated process.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600))
	require.Error(t, processControl(ctx, "reload"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(command.Process.Pid)), 0600))
	t.Log("real sshd staged validation, atomic drop-in, reload and cold reconciliation passed; administrator login preserved")
	// An Include is conditional when it appears inside a Match block. Staging
	// must preserve that scope instead of inlining its Match all content.
	conditional := *d
	conditional.Root = filepath.Join(dir, "conditional-journal")
	conditional.ConfigPath = filepath.Join(dir, "conditional-main")
	conditional.DropInPath = filepath.Join(dir, "conditional-managed")
	conditional.Reload = func(context.Context) error { t.Error("invalid conditional configuration reloaded"); return nil }
	conditionalConfig := strings.Replace(config, "Include "+drop, "Match User sysbox_nonexistent_user\nInclude "+conditional.DropInPath, 1)
	require.NoError(t, os.WriteFile(conditional.ConfigPath, []byte(conditionalConfig), 0600))
	require.ErrorContains(t, conditional.Ensure(ctx, []substrate.SSHAccessConfig{{Port: port}}), "configuration conflict")
	_, err = os.Stat(conditional.DropInPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}
