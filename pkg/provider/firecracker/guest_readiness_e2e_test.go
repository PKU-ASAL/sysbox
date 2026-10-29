//go:build e2e

package firecracker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
)

// Exercise the stock Ubuntu startup cleanup at a controlled delay. A ping
// must work while normal Exec/Copy are blocked; after readiness installation
// succeeds without replaying an effect or adding a blind delay to production.
func TestFirecrackerGuestInitializationBarrierE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "readiness", TopologyUID: "readiness", ResourceUID: "barrier", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "readiness-e2e", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	dropin := filepath.Join(t.TempDir(), "delay.conf")
	require.NoError(t, os.WriteFile(dropin, []byte("[Service]\nExecStartPre=/bin/sleep 12\n"), 0600))
	hs := h.Provider.(*HandleState)
	disk := filepath.Join(hs.VMDir, "rootfs.ext4")
	for _, cmd := range []string{"mkdir /etc/systemd/system/systemd-tmpfiles-setup.service.d", "write " + dropin + " /etc/systemd/system/systemd-tmpfiles-setup.service.d/readiness.conf"} {
		out, err := exec.CommandContext(ctx, "debugfs", "-w", "-R", cmd, disk).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	require.NoError(t, s.StartNode(ctx, h))
	require.False(t, hs.Ready, "RPC liveness cannot publish guest readiness")
	ping := readinessRPC(t, ctx, hs, vsockrpc.Request{Op: vsockrpc.OpPing})
	require.True(t, ping.Pong)
	waiting := readinessRPC(t, ctx, hs, vsockrpc.Request{Op: vsockrpc.OpReady})
	require.False(t, waiting.Ready)
	require.NotEmpty(t, waiting.ReadinessReason)
	denied := readinessRPC(t, ctx, hs, vsockrpc.Request{Op: vsockrpc.OpExec, Cmd: []string{"touch", "/root/premature-effect"}})
	require.Contains(t, denied.Error, "not ready")
	denied = readinessRPC(t, ctx, hs, vsockrpc.Request{Op: vsockrpc.OpWriteFile, Path: "/root/premature-file"})
	require.Contains(t, denied.Error, "not ready")
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, substrate.ExecRequest{Program: "touch", Args: []string{"/root/host-premature"}, Shell: substrate.ShellNone, Timeout: 100 * time.Millisecond}, nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	source := filepath.Join(t.TempDir(), "payload")
	require.NoError(t, os.WriteFile(source, []byte("verified startup payload\n"), 0600))
	copyCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err = conn.(substrate.ConnectionFileCopier).CopyFile(copyCtx, source, "/root/host-premature-file")
	stop()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, conn.(substrate.ConnectionWaiter).WaitReady(ctx, 30*time.Second))
	result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "test ! -e /root/premature-effect && test ! -e /root/premature-file && test ! -e /root/host-premature && test ! -e /root/host-premature-file", Shell: substrate.ShellLinux}, nil, nil)
	require.NoError(t, err)
	require.True(t, result.ExitCodeKnown)
	require.Zero(t, result.ExitCode)
	for i := 0; i < 20; i++ {
		require.NoError(t, s.CopyToNode(ctx, h, source, "/tmp/sysbox-readiness-file", 0640))
	}
	result, err = conn.Exec(ctx, substrate.ExecRequest{Program: "cat /tmp/sysbox-readiness-file; stat -c %a /tmp/sysbox-readiness-file", Shell: substrate.ShellLinux}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "verified startup payload\n640\n", result.Stdout)
	t.Log("ping remained live while tmpfiles startup was delayed; Exec/Copy produced no effects until readiness, then 20 installations succeeded")
}

func readinessRPC(t *testing.T, ctx context.Context, hs *HandleState, req vsockrpc.Request) vsockrpc.Frame {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", hs.VsockUDS)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	_, err = fmt.Fprintf(conn, "CONNECT %d\n", hs.VsockPort)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	_, err = reader.ReadString('\n')
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(conn).Encode(req))
	var frame vsockrpc.Frame
	require.NoError(t, json.NewDecoder(io.Reader(reader)).Decode(&frame))
	return frame
}
