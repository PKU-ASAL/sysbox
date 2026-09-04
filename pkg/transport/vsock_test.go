package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
)

// silentVsockPeer is a real UDS listener standing in for the firecracker vsock
// mux. It models the failure that matters: the peer accepts the connection and
// then stops talking. Nothing about that is visible at the socket layer — the
// connection is healthy, there is simply no data — so only a deadline or a
// context can end the wait.
type silentVsockPeer struct {
	path string

	mu    sync.Mutex
	conns []net.Conn

	// handshake replies "OK <port>" before going silent when true; when false
	// the peer never even answers CONNECT.
	handshake bool
}

// newSilentVsockPeer starts a listener under /tmp — unix socket paths are
// length-limited, and t.TempDir() is too long on darwin.
func newSilentVsockPeer(t *testing.T, handshake bool) *silentVsockPeer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vsock-test-")
	require.NoError(t, err)
	peer := &silentVsockPeer{path: filepath.Join(dir, "s"), handshake: handshake}

	ln, err := net.Listen("unix", peer.path)
	require.NoError(t, err)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			peer.mu.Lock()
			peer.conns = append(peer.conns, conn)
			peer.mu.Unlock()
			go peer.serve(conn)
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()
		peer.mu.Lock()
		for _, c := range peer.conns {
			_ = c.Close()
		}
		peer.mu.Unlock()
		_ = os.RemoveAll(dir)
	})
	return peer
}

func (p *silentVsockPeer) serve(conn net.Conn) {
	if !p.handshake {
		return // hold the connection open and say nothing at all
	}
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // the CONNECT line
		return
	}
	if _, err := io.WriteString(conn, "OK 1024\n"); err != nil {
		return
	}
	// Drain the exec request, then go silent: the agent accepted the work and
	// never reports a frame, which is what a hung guest command looks like.
	var req vsockrpc.Request
	_ = json.NewDecoder(br).Decode(&req)
}

// A peer that accepts the connection but never answers CONNECT must not hang
// the caller. net.Dialer.DialContext bounds only the connect; the handshake
// read that follows is on a bare conn with no deadline, so without an explicit
// bound this blocks forever regardless of ctx.
func TestVsockDialDoesNotHangWhenPeerNeverAnswers(t *testing.T) {
	peer := newSilentVsockPeer(t, false)
	vc := NewVsockConnection(peer.path, 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := vc.dial(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "dial must fail rather than wait on a silent peer")
	case <-time.After(3 * time.Second):
		t.Fatal("dial never returned; the CONNECT handshake read is unbounded")
	}
}

// The same defect one layer up: after a successful handshake, execFrameStream
// blocks in json.Decode on a bare conn. The context is not consulted, so a
// guest command that never writes and never exits makes TimeoutSeconds
// unenforceable on the firecracker/vsock path — exactly the failure that was
// fixed for the docker path.
func TestVsockExecAbortsWhenAgentNeverSendsAFrame(t *testing.T) {
	peer := newSilentVsockPeer(t, true)
	vc := NewVsockConnection(peer.path, 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := vc.Exec(ctx, substrate.ExecRequest{Program: "sleep", Args: []string{"600"}, Shell: substrate.ShellNone}, io.Discard, io.Discard)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a silent agent must surface as an error, not as success")
	case <-time.After(3 * time.Second):
		t.Fatal("Exec never returned; the frame read is unbounded and timeout_seconds cannot be enforced")
	}
}
