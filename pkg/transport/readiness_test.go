package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func readinessPeer(t *testing.T, ready *atomic.Bool, effects *atomic.Int32) *VsockConnection {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ready")
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
				r := bufio.NewReader(conn)
				if _, err := r.ReadString('\n'); err != nil {
					return
				}
				io.WriteString(conn, "OK 1\n")
				line, err := r.ReadBytes('\n')
				if err != nil {
					return
				}
				var req vsockrpc.Request
				if json.Unmarshal(line, &req) != nil {
					return
				}
				var response any
				switch string(req.Op) {
				case "ping":
					response = map[string]any{"done": true, "pong": true}
				case "ready.v1":
					response = map[string]any{"done": true, "ready": ready.Load(), "readiness_reason": "guest booting"}
				default:
					effects.Add(1)
					response = vsockrpc.Frame{Done: true}
				}
				json.NewEncoder(conn).Encode(response)
			}()
		}
	}()
	return NewVsockConnection(path, 1)
}

func TestReadinessDoesNotAcceptLivenessPong(t *testing.T) {
	var ready atomic.Bool
	var effects atomic.Int32
	conn := readinessPeer(t, &ready, &effects)
	err := conn.WaitReady(context.Background(), 35*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, effects.Load())
	ready.Store(true)
	require.NoError(t, conn.WaitReady(context.Background(), time.Second))
}

func TestExecWaitsForGuestInitializationBeforeEffect(t *testing.T) {
	var ready atomic.Bool
	var effects atomic.Int32
	conn := readinessPeer(t, &ready, &effects)
	_, err := conn.Exec(context.Background(), substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone, Timeout: 35 * time.Millisecond}, nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, effects.Load())
	ready.Store(true)
	_, err = conn.Exec(context.Background(), substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone}, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, effects.Load())
}
