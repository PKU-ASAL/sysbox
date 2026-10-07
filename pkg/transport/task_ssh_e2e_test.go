//go:build e2e

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
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

func TestSSHDurableGuestTaskE2E(t *testing.T) {
	helper := os.Getenv("SYSBOX_GUESTTASK_HELPER")
	if helper == "" {
		t.Skip("SYSBOX_GUESTTASK_HELPER current static sysbox-init binary required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	run := func(binary string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keypath := filepath.Join(dir, "grant.pub")
	require.NoError(t, os.WriteFile(keypath, pub, 0600))
	container := run("docker", "run", "-d", "--rm", "--privileged", "--cgroupns=private", "--network", "none", "-v", helper+":/usr/local/bin/sysbox-guest-task:ro", "-v", keypath+":/etc/sysbox/grant-public-key:ro", "alpine:3.22", "sleep", "120")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	hostkey, clientkey := filepath.Join(dir, "host"), filepath.Join(dir, "client")
	run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostkey)
	run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", clientkey)
	authorized, err := os.ReadFile(clientkey + ".pub")
	require.NoError(t, err)
	authorizedPath := filepath.Join(dir, "authorized_keys")
	require.NoError(t, os.WriteFile(authorizedPath, authorized, 0600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	u, err := user.Current()
	require.NoError(t, err)
	docker, err := exec.LookPath("docker")
	require.NoError(t, err)
	config := fmt.Sprintf("ListenAddress 127.0.0.1\nPort %d\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\nAllowUsers %s\nForceCommand %s exec -i %s /usr/local/bin/sysbox-guest-task --guest-task\n", port, hostkey, filepath.Join(dir, "sshd.pid"), authorizedPath, u.Username, docker, container)
	configPath := filepath.Join(dir, "sshd_config")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0600))
	log, err := os.Create(filepath.Join(dir, "sshd.log"))
	require.NoError(t, err)
	defer log.Close()
	server := exec.CommandContext(ctx, "/usr/sbin/sshd", "-D", "-e", "-f", configPath)
	server.Stderr = log
	require.NoError(t, server.Start())
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		return false
	}, 3*time.Second, 20*time.Millisecond)
	hostpub, err := os.ReadFile(hostkey + ".pub")
	require.NoError(t, err)
	known := filepath.Join(dir, "known_hosts")
	require.NoError(t, os.WriteFile(known, []byte(fmt.Sprintf("[127.0.0.1]:%d %s", port, hostpub)), 0600))
	conn := NewSSHConnectionWithTrust("127.0.0.1", fmt.Sprint(port), u.Username, clientkey, "", known, false)
	conn.BindExecutionIdentity("node", 1)
	boot, err := conn.GuestTaskBootID(ctx)
	if err != nil {
		raw, _ := os.ReadFile(filepath.Join(dir, "sshd.log"))
		t.Log(string(raw))
	}
	require.NoError(t, err)
	req := substrate.ExecRequest{Program: "/bin/sh", Args: []string{"-c", "printf ssh-task; exit 7"}, Shell: substrate.ShellNone}
	task, err := BuildTaskRequest(ctx, "ssh-task", req)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{TargetBootID: boot, Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "ssh-task", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "guest_task", Digest: digest, ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive}
	signed, err := controlplane.SignExecutionGrant(key, grant, boot, time.Now())
	require.NoError(t, err)
	h, err := conn.Start(WithTaskGrant(ctx, task.ID, signed), req)
	require.NoError(t, err)
	again, err := conn.Start(WithTaskGrant(ctx, task.ID, signed), req)
	require.NoError(t, err)
	require.Equal(t, h.ID, again.ID)
	var result substrate.ExecResult
	require.Eventually(t, func() bool { result, err = conn.ObserveExecution(ctx, h); return err == nil && result.ExitCodeKnown }, 5*time.Second, 40*time.Millisecond)
	require.Equal(t, 7, result.ExitCode)
	output, err := conn.ReadExecutionOutput(ctx, h, "stdout", 0, 100)
	require.NoError(t, err)
	require.Equal(t, "ssh-task", string(output.Data))
	run("docker", "exec", container, "sh", "-c", `mkdir -p /var/tmp/sysbox-install /etc/sysbox; chmod 700 /var/tmp/sysbox-install; printf '["/var/tmp/sysbox-install"]' > /etc/sysbox/install-roots.json; chmod 600 /etc/sysbox/install-roots.json`)
	fileBody := bytes.Repeat([]byte("file-proof"), 1024*1024)
	fileReq := substrate.FileInstallRequest{ID: "ssh-file", Path: "/var/tmp/sysbox-install/data", Mode: 0640, Size: int64(len(fileBody)), SHA256: fmt.Sprintf("%x", sha256.Sum256(fileBody))}
	fileDigest, err := fileReq.Digest()
	require.NoError(t, err)
	fileGrant := grant
	fileGrant.Scope.ResourceUID = "file-resource"
	fileGrant.ActionID = fileReq.ID
	fileGrant.CommandID = "file-command"
	fileGrant.Operation = "guest_file_install"
	fileGrant.Digest = fileDigest
	fileSigned, err := controlplane.SignExecutionGrant(key, fileGrant, boot, time.Now())
	require.NoError(t, err)
	fileHandle, fileResult, err := conn.InstallFile(WithTaskGrant(ctx, fileReq.ID, fileSigned), fileReq, bytes.NewReader(fileBody))
	require.NoError(t, err)
	require.True(t, fileResult.Quiescent)
	require.Equal(t, substrate.FileInstallCommitted, fileResult.Outcome)
	reopened := NewSSHConnectionWithTrust("127.0.0.1", fmt.Sprint(port), u.Username, clientkey, "", known, false)
	reopened.BindExecutionIdentity("node", 1)
	cold, err := reopened.ObserveFileInstall(ctx, fileHandle)
	require.NoError(t, err)
	require.Equal(t, fileResult, cold)
	require.Equal(t, fileReq.SHA256, strings.Fields(run("docker", "exec", container, "sha256sum", fileReq.Path))[0])
	require.Equal(t, "640", run("docker", "exec", container, "stat", "-c", "%a", fileReq.Path))
	t.Log("real pinned SSH transport -> isolated Docker guest helper: durable task start/dedup/observe/output verified")
	t.Log("10MiB file installed through signed fixed helper protocol; cold Observe, actual checksum and mode verified")
}
