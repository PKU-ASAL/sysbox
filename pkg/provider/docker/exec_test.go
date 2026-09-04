package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAtomicRenameRequestUsesDirectExecution(t *testing.T) {
	req := atomicRenameRequest("/etc/.sysbox-proof", "/etc/proof")

	require.Equal(t, "mv", req.Program)
	require.Equal(t, []string{"-f", "/etc/.sysbox-proof", "/etc/proof"}, req.Args)
	require.Equal(t, substrate.ShellNone, req.Shell)
}

// hijackedStream models Docker's hijacked exec connection: it delivers whatever
// framed bytes it was given, and then — like the real connection — blocks until
// something closes it. Cancelling the request context does not close it, which
// is why a hung guest command can outlive any deadline.
//
// endErr models the read failure a mid-stream close actually produces
// ("use of closed network connection"); with endErr nil the stream instead
// reports a clean EOF, which is the frame-boundary case.
type hijackedStream struct {
	closeOnce sync.Once
	closed    chan struct{}
	data      *bytes.Reader
	endErr    error
}

func newHijackedStream() *hijackedStream {
	return &hijackedStream{closed: make(chan struct{}), data: bytes.NewReader(nil)}
}

// newFramedStream builds a stream carrying real stdcopy frames, so that a test
// can tell correct demultiplexing apart from swapped, dropped or truncated
// output.
func newFramedStream(stdout, stderr string) *hijackedStream {
	var framed bytes.Buffer
	if stdout != "" {
		_, _ = stdcopy.NewStdWriter(&framed, stdcopy.Stdout).Write([]byte(stdout))
	}
	if stderr != "" {
		_, _ = stdcopy.NewStdWriter(&framed, stdcopy.Stderr).Write([]byte(stderr))
	}
	return &hijackedStream{closed: make(chan struct{}), data: bytes.NewReader(framed.Bytes())}
}

// failWith makes the stream report err once its framed bytes are exhausted,
// modelling a connection torn down mid-stream rather than ending cleanly.
func (s *hijackedStream) failWith(err error) *hijackedStream {
	s.endErr = err
	return s
}

func (s *hijackedStream) Read(p []byte) (int, error) {
	if s.data != nil && s.data.Len() > 0 {
		return s.data.Read(p)
	}
	if s.endErr != nil {
		return 0, s.endErr
	}
	<-s.closed
	return 0, io.EOF
}

func (s *hijackedStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
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
//
// The stream therefore carries a real (partial) payload: the point is not that
// an empty read fails, but that output already in hand does not launder an
// expired context into success.
func TestReadExecStreamsDoesNotReportTruncatedOutputAsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Frames arrive, then a clean EOF — StdCopy returns a nil error.
	stream := newFramedStream("partial body, cut off mid-", "")
	require.NoError(t, stream.Close())

	stdout, _, err := readExecStreams(ctx, stream, stream)

	require.Error(t, err, "a nil read error under an expired context must not be success")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "partial body, cut off mid-", stdout.String(),
		"the partial body must still be returned so the caller can report what was seen")
}

// A read failure that is not the context's doing must reach the caller intact:
// this is the dominant real-world abort signature, and swallowing it would hide
// a genuine daemon or protocol defect.
func TestReadExecStreamsPropagatesReadError(t *testing.T) {
	readErr := errors.New("use of closed network connection")
	stream := newFramedStream("some output", "").failWith(readErr)

	_, _, err := readExecStreams(context.Background(), stream, stream)

	require.ErrorIs(t, err, readErr)
}

// The happy path must not be disturbed by the abort machinery, and it must
// prove the two streams are demultiplexed into the right buffers — otherwise a
// wired-backwards, dropped or truncated payload is indistinguishable from
// correct behaviour.
func TestReadExecStreamsReturnsPayloadWhenStreamEnds(t *testing.T) {
	stream := newFramedStream("out-payload", "err-payload")
	require.NoError(t, stream.Close())

	stdout, stderr, err := readExecStreams(context.Background(), stream, stream)

	require.NoError(t, err)
	require.Equal(t, "out-payload", stdout.String())
	require.Equal(t, "err-payload", stderr.String())
}
