package docker

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAtomicRenameRequestUsesDirectExecution(t *testing.T) {
	req := atomicRenameRequest("/etc/.sysbox-proof", "/etc/proof")

	require.Equal(t, "mv", req.Program)
	require.Equal(t, []string{"-f", "/etc/.sysbox-proof", "/etc/proof"}, req.Args)
	require.Equal(t, substrate.ShellNone, req.Shell)
}

// hijackedStream models Docker's hijacked exec connection: reads block until the
// connection itself is closed. Cancelling the request context does not close it,
// which is why a hung guest command can outlive any deadline.
type hijackedStream struct {
	closed chan struct{}
}

func newHijackedStream() *hijackedStream {
	return &hijackedStream{closed: make(chan struct{})}
}

func (s *hijackedStream) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}

func (s *hijackedStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// A guest command that never writes and never exits must not outlive the
// context. Docker's hijacked connection ignores ctx, so the read has to be
// aborted by closing the connection — otherwise ExecInNode blocks forever and
// GuestExecutionRequest.TimeoutSeconds is unenforceable. This is what let an
// edge-local check with timeout_seconds=10 run for 134 seconds.
func TestReadExecStreamsAbortsWhenContextIsDone(t *testing.T) {
	stream := newHijackedStream()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	type outcome struct{ err error }
	done := make(chan outcome, 1)
	go func() {
		_, _, err := readExecStreams(ctx, stream, stream)
		done <- outcome{err: err}
	}()

	select {
	case got := <-done:
		require.ErrorIs(t, got.err, context.DeadlineExceeded)
	case <-time.After(3 * time.Second):
		t.Fatal("readExecStreams did not return after the context expired; the hijacked read was never aborted")
	}
}

// A close landing on a frame boundary makes StdCopy report a clean EOF rather
// than use-of-closed-conn, so a nil read error is not proof the stream was
// complete. Truncated output must never be reported as success: a service check
// would draw a conclusion from a partial body.
func TestReadExecStreamsDoesNotReportTruncatedOutputAsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Already at EOF, so StdCopy returns a nil error — the frame-boundary case.
	stream := newHijackedStream()
	require.NoError(t, stream.Close())

	_, _, err := readExecStreams(ctx, stream, stream)

	require.Error(t, err, "a nil read error under an expired context must not be success")
	require.ErrorIs(t, err, context.Canceled)
}

// The happy path must not be disturbed by the abort machinery: a stream that
// closes on its own returns its payload with no error.
func TestReadExecStreamsReturnsPayloadWhenStreamEnds(t *testing.T) {
	stream := newHijackedStream()
	require.NoError(t, stream.Close())

	stdout, stderr, err := readExecStreams(context.Background(), stream, stream)

	require.NoError(t, err)
	require.Empty(t, stdout.String())
	require.Empty(t, stderr.String())
}
