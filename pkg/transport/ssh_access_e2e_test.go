//go:build e2e

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type sshAccessDockerConnection struct{ container string }

func (c sshAccessDockerConnection) Exec(ctx context.Context, req substrate.ExecRequest, _, _ io.Writer) (substrate.ExecResult, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", c.container, req.Program}, req.Args...)...)
	cmd.Stdin = req.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("guest helper: %w: %s", err, stderr.String())
	}
	return substrate.ExecResult{ExitCodeKnown: true, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}
func (sshAccessDockerConnection) CopyTo(context.Context, string, string, uint32) error {
	return fmt.Errorf("unused")
}
func (sshAccessDockerConnection) Probe(context.Context) error { return nil }

// The disposable sshd uses Docker only as an isolation adapter for its fixed
// AuthorizedKeysCommand. No host SSH configuration or user key files change.
func TestSSHAccessRealRotationAndSharedLoginE2E(t *testing.T) {
	helper := os.Getenv("SYSBOX_GUESTTASK_HELPER")
	if helper == "" {
		t.Skip("SYSBOX_GUESTTASK_HELPER current static sysbox-init binary required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := t.TempDir()
	run := func(binary string, args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		require.NoError(t, err, string(output))
		return strings.TrimSpace(string(output))
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicPath := filepath.Join(dir, "grant.pub")
	require.NoError(t, os.WriteFile(publicPath, pub, 0600))
	container := run("docker", "run", "-d", "--rm", "--network", "none", "-v", helper+":/usr/local/bin/sysbox-guest-task:ro", "-v", publicPath+":/etc/sysbox/grant-public-key:ro", "alpine:3.22", "sleep", "120")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	client := NewTaskClient(sshAccessDockerConnection{container}, "node", 1)
	boot, err := client.GuestFileBootID(ctx)
	require.NoError(t, err)
	currentUser, err := user.Current()
	require.NoError(t, err)
	keys := make(map[string]string)
	publics := make(map[string]string)
	for _, name := range []string{"host", "administrator", "first", "rotated", "rescue"} {
		keys[name] = filepath.Join(dir, name)
		run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keys[name])
		raw, err := os.ReadFile(keys[name] + ".pub")
		require.NoError(t, err)
		publics[name] = strings.TrimSpace(string(raw))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	docker, err := exec.LookPath("docker")
	require.NoError(t, err)
	config := fmt.Sprintf("ListenAddress 127.0.0.1\nPort %d\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nAuthorizedKeysCommand %s exec %s /usr/local/bin/sysbox-guest-task --ssh-authorized-keys %%u\nAuthorizedKeysCommandUser %s\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\nAllowUsers %s\n", port, keys["host"], filepath.Join(dir, "sshd.pid"), keys["administrator"]+".pub", docker, container, currentUser.Username, currentUser.Username)
	configPath := filepath.Join(dir, "sshd_config")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0600))
	run("/usr/sbin/sshd", "-t", "-f", configPath)
	log, err := os.Create(filepath.Join(dir, "sshd.log"))
	require.NoError(t, err)
	defer log.Close()
	server := exec.CommandContext(ctx, "/usr/sbin/sshd", "-D", "-e", "-f", configPath)
	server.Stderr = log
	require.NoError(t, server.Start())
	t.Cleanup(func() { _ = server.Process.Kill(); _ = server.Wait() })
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 3*time.Second, 20*time.Millisecond)
	knownHosts := filepath.Join(dir, "known_hosts")
	require.NoError(t, os.WriteFile(knownHosts, []byte(fmt.Sprintf("[127.0.0.1]:%d %s\n", port, publics["host"])), 0600))
	login := func(name string, allowed bool) {
		t.Helper()
		command := exec.CommandContext(ctx, "ssh", "-p", fmt.Sprint(port), "-i", keys[name], "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "PreferredAuthentications=publickey", "-o", "ConnectTimeout=3", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+knownHosts, currentUser.Username+"@127.0.0.1", "true")
		output, err := command.CombinedOutput()
		if allowed {
			if err != nil {
				raw, _ := os.ReadFile(filepath.Join(dir, "sshd.log"))
				t.Log(string(raw))
			}
			require.NoError(t, err, string(output))
		} else {
			require.Error(t, err)
			require.Contains(t, string(output), "Permission denied", "transport errors are not proof of key rejection")
		}
	}
	mutate := func(req substrate.SSHAccessMutation, epoch uint64) (substrate.SSHAccessResult, error) {
		t.Helper()
		digest, err := req.Digest()
		require.NoError(t, err)
		op := "guest_ssh_access_apply"
		if req.Delete {
			op = "guest_ssh_access_delete"
		}
		grant, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "ssh-e2e", ResourceUID: "node", Generation: 1}, TargetBootID: boot, AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: req.ID, Attempt: 1, Epoch: epoch, Operation: op, Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, boot, time.Now())
		require.NoError(t, err)
		return client.MutateSSHAccess(WithTaskGrant(ctx, req.ID, grant), req)
	}
	login("administrator", true)
	login("first", false)
	req := substrate.SSHAccessMutation{ID: "create", Config: substrate.SSHAccessConfig{User: currentUser.Username, AccessID: "admin", AuthorizedKeys: []string{publics["first"]}}}
	first, err := mutate(req, 1)
	require.NoError(t, err)
	login("first", true)
	rescue := req
	rescue.ID, rescue.Config.AccessID = "rescue", "rescue"
	rescue.Config.AuthorizedKeys = []string{publics["rescue"]}
	_, err = mutate(rescue, 2)
	require.NoError(t, err)
	req.ID, req.ExpectedRevision = "rotate", 1
	req.Config.AuthorizedKeys = []string{publics["rotated"]}
	_, err = mutate(req, 3)
	require.NoError(t, err)
	login("first", false)
	login("rotated", true)
	login("rescue", true)
	login("administrator", true)
	remove := substrate.SSHAccessMutation{ID: "remove", ExpectedRevision: 1, Delete: true, Config: substrate.SSHAccessConfig{User: currentUser.Username, AccessID: "admin"}}
	_, err = mutate(remove, 4)
	require.Error(t, err)
	login("rotated", true)
	remove.ExpectedRevision = 2
	_, err = mutate(remove, 4)
	require.NoError(t, err)
	login("rotated", false)
	login("rescue", true)
	login("administrator", true)
	historical, err := client.ObserveSSHAccess(ctx, first.Handle)
	require.NoError(t, err)
	require.Equal(t, first, historical)
	inspection, err := client.InspectSSHAccess(ctx, first.Handle)
	require.NoError(t, err)
	require.Equal(t, "absent", inspection.Status)
	require.Equal(t, uint64(3), inspection.Revision)
	t.Log("real sshd: old key revoked, replacement removed, independent managed and administrator logins preserved; guest helper reopened for every call")
}
