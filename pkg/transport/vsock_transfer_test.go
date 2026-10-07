package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func transferPeer(t *testing.T, serve func(net.Conn, *bufio.Reader, vsockrpc.Request)) *VsockConnection {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vsock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				if _, err := reader.ReadString('\n'); err != nil {
					return
				}
				if _, err := io.WriteString(conn, "OK 1024\n"); err != nil {
					return
				}
				header, err := reader.ReadBytes('\n')
				if err != nil {
					return
				}
				var req vsockrpc.Request
				if json.Unmarshal(header, &req) != nil {
					return
				}
				if req.Op == vsockrpc.OpReady {
					_ = json.NewEncoder(conn).Encode(vsockrpc.Frame{Done: true, Ready: true})
					return
				}
				serve(conn, reader, req)
			}()
		}
	}()
	return NewVsockConnection(path, 1024)
}

func TestVsockExecTransmitsBoundedStdin(t *testing.T) {
	input := []byte("input\x00with newline\n")
	c := transferPeer(t, func(conn net.Conn, r *bufio.Reader, req vsockrpc.Request) {
		body, err := io.ReadAll(io.LimitReader(r, req.StdinSize))
		if err != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(vsockrpc.Frame{Stdout: body})
		_ = json.NewEncoder(conn).Encode(vsockrpc.Frame{Done: true})
	})
	result, err := c.Exec(context.Background(), substrate.ExecRequest{Program: "cat", Shell: substrate.ShellNone, Stdin: bytes.NewReader(input)}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, string(input), result.Stdout)
	require.True(t, result.ExitCodeKnown)
}

func TestVsockCopyRequiresMatchingTerminalChecksum(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0600))
	for _, bad := range []bool{true, false} {
		c := transferPeer(t, func(conn net.Conn, r *bufio.Reader, req vsockrpc.Request) {
			_, _ = io.CopyN(io.Discard, r, req.Size)
			digest := req.SHA256
			if bad {
				digest = "wrong"
			}
			_ = json.NewEncoder(conn).Encode(vsockrpc.Frame{Done: true, SHA256: digest})
		})
		err := c.CopyFile(context.Background(), src, "/tmp/target")
		if bad {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestVsockCopyCancellationUnblocksSilentAcknowledgement(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0600))
	c := transferPeer(t, func(conn net.Conn, r *bufio.Reader, req vsockrpc.Request) { _, _ = io.Copy(io.Discard, r) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.CopyFile(ctx, src, "/tmp/target") }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("copy ignored cancellation")
	}
}

func TestVsockExecCancellationClosesStreamingStdin(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewVsockConnection("/unused", 1024).Exec(ctx, substrate.ExecRequest{Program: "cat", Shell: substrate.ShellNone, Stdin: r}, nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
