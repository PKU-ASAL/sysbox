package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
)

func framePeer(t *testing.T, frames ...vsockrpc.Frame) *VsockConnection {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
		if _, err := io.WriteString(conn, "OK 1024\n"); err != nil {
			return
		}
		var req vsockrpc.Request
		if json.NewDecoder(reader).Decode(&req) != nil {
			return
		}
		for _, frame := range frames {
			if json.NewEncoder(conn).Encode(frame) != nil {
				return
			}
		}
	}()
	return NewVsockConnection(path, 1024)
}

func TestVsockExecutionBoundsOutputAndPreservesNonzeroExit(t *testing.T) {
	conn := framePeer(t, vsockrpc.Frame{Stdout: []byte("abcdef"), Stderr: []byte("12345")}, vsockrpc.Frame{Done: true, ExitCode: 7})
	var stdout, stderr bytes.Buffer
	result, err := conn.Exec(context.Background(), substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone, OutputLimitBytes: 3}, &stdout, &stderr)
	require.NoError(t, err)
	require.Equal(t, "abc", result.Stdout)
	require.Equal(t, "123", result.Stderr)
	require.Equal(t, result.Stdout, stdout.String())
	require.True(t, result.StdoutTruncated)
	require.True(t, result.StderrTruncated)
	require.Equal(t, 7, result.ExitCode)
	require.True(t, result.ExitCodeKnown)
	require.Equal(t, substrate.ExecStatusExited, result.Status)
}

func TestVsockExecutionDoesNotHideAgentErrorBehindExitCode(t *testing.T) {
	conn := framePeer(t, vsockrpc.Frame{Done: true, ExitCode: 7, Error: "agent execution failed"})
	result, err := conn.Exec(context.Background(), substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone}, nil, nil)
	require.ErrorContains(t, err, "agent execution failed")
	require.False(t, result.ExitCodeKnown)
}

func TestVsockExecutionTimeoutDoesNotClaimRemoteTermination(t *testing.T) {
	peer := newSilentVsockPeer(t, true)
	result, err := NewVsockConnection(peer.path, 1024).Exec(context.Background(), substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone, Timeout: 10 * time.Millisecond}, nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, substrate.ExecErrorTimeout, result.ErrorClass)
	require.Equal(t, substrate.RemoteTerminationUnknown, result.RemoteTermination)
	require.False(t, result.ExitCodeKnown)
}

func TestVsockRejectsOversizedStdinBeforeDial(t *testing.T) {
	_, err := NewVsockConnection("/does-not-exist", 1024).Exec(context.Background(), substrate.ExecRequest{Program: "cat", Shell: substrate.ShellNone, Stdin: strings.NewReader(strings.Repeat("x", vsockrpc.MaxStdinBytes+1))}, nil, nil)
	require.ErrorContains(t, err, "stdin exceeds")
}

func TestSSHExecutionBoundsOutputAtWriter(t *testing.T) {
	conn := NewSSHConnectionSecure("unused", "22", "user", "", "")
	conn.runCommand = func(cmd *exec.Cmd) error {
		_, err := io.WriteString(cmd.Stdout, "abcdef")
		return err
	}
	var output bytes.Buffer
	result, err := conn.Exec(context.Background(), substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone, OutputLimitBytes: 3}, &output, nil)
	require.NoError(t, err)
	require.Equal(t, "abc", result.Stdout)
	require.Equal(t, "abc", output.String())
	require.True(t, result.StdoutTruncated)
	require.True(t, result.ExitCodeKnown)
}

func TestSSHExecutionCancellationRetainsOutputWithoutClaimingRemoteExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := NewSSHConnectionSecure("unused", "22", "user", "", "")
	conn.runCommand = func(cmd *exec.Cmd) error {
		_, _ = io.WriteString(cmd.Stdout, "partial")
		cancel()
		return errors.New("signal: killed")
	}
	result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone}, nil, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "partial", result.Stdout)
	require.Equal(t, substrate.ExecErrorCanceled, result.ErrorClass)
	require.Equal(t, substrate.RemoteTerminationUnknown, result.RemoteTermination)
	require.False(t, result.ExitCodeKnown)
}

func TestVsockRejectsOversizedProtocolFrame(t *testing.T) {
	conn := framePeer(t, vsockrpc.Frame{Stdout: []byte(strings.Repeat("x", 128<<10))}, vsockrpc.Frame{Done: true})
	result, err := conn.Exec(context.Background(), substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone, OutputLimitBytes: 8}, nil, nil)
	require.Error(t, err)
	require.False(t, result.ExitCodeKnown)
}

func TestSSHStartReturnsOpaqueIdentityAndUsesConnectionCommand(t *testing.T) {
	conn := NewSSHConnectionInNamespaceWithTrust("lab", "unused", "22", "user", "/key", "", "/known", false)
	conn.BindExecutionIdentity("node-1", 4)
	var args []string
	conn.runCommand = func(cmd *exec.Cmd) error {
		args = append([]string{}, cmd.Args...)
		_, err := io.WriteString(cmd.Stdout, "1234\n")
		return err
	}
	handle, err := conn.Start(context.Background(), substrate.ExecRequest{Program: "echo", Args: []string{"$unsafe"}, Environment: map[string]string{"A": "x y"}, WorkingDir: "/work path", Shell: substrate.ShellNone})
	require.NoError(t, err)
	require.NotEmpty(t, handle.ID)
	require.NotEqual(t, "1234", handle.ID)
	require.Equal(t, "node-1", handle.NodeID)
	require.EqualValues(t, 4, handle.Generation)
	require.Equal(t, 1234, handle.PID)
	require.Equal(t, []string{"ip", "netns", "exec", "lab"}, args[:4])
	require.Contains(t, args, "StrictHostKeyChecking=yes")
	require.Contains(t, args, "UserKnownHostsFile=/known")
	require.Contains(t, args[len(args)-1], "nohup sh -c")
}

func TestVsockStartRejectsMissingPID(t *testing.T) {
	conn := framePeer(t, vsockrpc.Frame{Stdout: []byte("not a pid\n")}, vsockrpc.Frame{Done: true})
	_, err := conn.Start(context.Background(), substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone})
	require.Error(t, err)
}

func TestVsockReadinessBudgetBoundsSilentPing(t *testing.T) {
	peer := newSilentVsockPeer(t, true)
	conn := NewVsockConnection(peer.path, 1024)
	done := make(chan error, 1)
	go func() { done <- conn.WaitReady(context.Background(), 10*time.Millisecond) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("readiness budget did not bound silent ping")
	}
}

func TestLegacySSHConstructorsRequireHostTrust(t *testing.T) {
	for _, conn := range []*SSHConnection{NewSSHConnection("host", "root", "", ""), NewSSHConnectionWithPort("host", "22", "root", "", ""), NewSSHConnectionInNamespace("ns", "host", "22", "root", "", "")} {
		require.Contains(t, conn.sshArgs(), "StrictHostKeyChecking=yes")
	}
}
