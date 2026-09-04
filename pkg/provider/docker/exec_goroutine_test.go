package docker

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The abort goroutine must not outlive the call. It selects on ctx.Done() and a
// local channel; if the deferred close were ever dropped, a long-lived context
// (the common case — ExecInNode is called with a request context that outlives
// one exec) would leak one goroutine per execution.
func TestReadExecStreamsLeavesNoGoroutineBehind(t *testing.T) {
	// A context that is never cancelled: the goroutine can only exit via the
	// deferred close.
	ctx := context.Background()

	settle := func() {
		for i := 0; i < 50; i++ {
			runtime.GC()
			time.Sleep(2 * time.Millisecond)
		}
	}

	settle()
	before := runtime.NumGoroutine()

	for i := 0; i < 200; i++ {
		stream := newHijackedStream()
		require.NoError(t, stream.Close())
		_, _, err := readExecStreams(ctx, stream, stream)
		require.NoError(t, err)
	}

	settle()
	after := runtime.NumGoroutine()

	// Allow a small margin for unrelated runtime goroutines rather than
	// asserting exact equality.
	require.LessOrEqual(t, after-before, 5,
		"goroutines grew from %d to %d across 200 calls; the abort goroutine is leaking", before, after)
}
