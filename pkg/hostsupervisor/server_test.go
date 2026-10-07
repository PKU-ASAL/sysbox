package hostsupervisor

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketClientUsesCanonicalTargetAndRecoversLostCompletion(t *testing.T) {
	dispatcher, request, provider := taskFixture(t)
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	require.NoError(t, os.Chmod(filepath.Dir(socket), 0700))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := Server{Socket: socket, GroupID: -1, Dispatcher: dispatcher}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Error("supervisor shutdown hung")
		}
	})
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("unix", socket, 20*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		return false
	}, time.Second, 10*time.Millisecond)
	client := NewTaskClient(socket, testRef(request))
	exec := substrate.ExecRequest{Program: request.Args[0], Shell: substrate.ShellNone, OutputLimitBytes: request.OutputLimit}
	// Keep recovery read-only for this completed fixture while the service runs.
	provider.result = substrate.ExecResult{ExitCode: 7, ExitCodeKnown: true, RemoteTermination: substrate.RemoteTerminationConfirmed}
	handle, err := client.Start(transport.WithTaskGrant(ctx, request.ID, request.Grant), exec)
	require.NoError(t, err)
	result, err := client.ObserveExecution(ctx, handle)
	require.NoError(t, err)
	require.True(t, result.ExitCodeKnown)
	require.Equal(t, 7, result.ExitCode)
	wrong := testRef(request)
	wrong.Scope.Generation++
	_, err = NewTaskClient(socket, wrong).CancelExecution(ctx, handle)
	require.Error(t, err)
	require.Zero(t, provider.cancels)
}

func TestSupervisorDoesNotReplaceExistingNonSocket(t *testing.T) {
	dispatcher, _, _ := taskFixture(t)
	path := filepath.Join(t.TempDir(), "keep")
	require.NoError(t, os.Chmod(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("original"), 0600))
	err := (&Server{Socket: path, GroupID: -1, Dispatcher: dispatcher}).Serve(context.Background())
	require.Error(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "original", string(raw))
}

func TestClientCancellationClosesBlockedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "silent")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			buf := make([]byte, 1024)
			for {
				if _, err := conn.Read(buf); err != nil {
					return
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = NewTaskClient(path, TargetRef{}).GuestTaskBootID(ctx)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)
}

type failedAcceptListener struct {
	conn net.Conn
	err  error
}

func (l *failedAcceptListener) Accept() (net.Conn, error) {
	if l.conn != nil {
		c := l.conn
		l.conn = nil
		return c, nil
	}
	return nil, l.err
}
func (*failedAcceptListener) Close() error   { return nil }
func (*failedAcceptListener) Addr() net.Addr { return &net.UnixAddr{Name: "test", Net: "unix"} }

func TestAcceptFailureCancelsRecoveryAndBlockedConnections(t *testing.T) {
	dispatcher, _, _ := taskFixture(t)
	server := Server{Dispatcher: dispatcher}
	accepted, client := net.Pipe()
	defer client.Close()
	failure := errors.New("listener failed")
	listener := &failedAcceptListener{conn: accepted, err: failure}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.serveListener(ctx, listener) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, failure)
		require.NoError(t, ctx.Err())
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("accept failure waited for parent cancellation")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, err := client.Read(make([]byte, 1))
	require.Error(t, err)
}
