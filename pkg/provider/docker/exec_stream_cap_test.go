package docker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The hijacked exec read drains into in-memory buffers with no bound, and
// stdcopy.StdCopy keeps writing until the stream ends. A guest command that
// floods its output — cat /dev/urandom | base64, a runaway log tail — therefore
// grows those buffers without limit for as long as the timeout allows, which is
// exactly the case the ctx abort now bounds in time but not in volume: ten
// seconds of a fast writer is easily gigabytes, and .String() then copies it
// again.
//
// The read must cap what it retains. The 1 MiB guestOutputLimit enforced by the
// agent is the authoritative truncation signal, so the ceiling here is larger;
// it only bounds the pathological case where the buffer would otherwise hold the
// whole stream.
func TestReadExecStreamsCapsRetainedOutput(t *testing.T) {
	payload := "abcdefghijklmnopqrstuvwxyz0123456789" // 36 bytes
	stream := newFramedStream(payload, "")
	require.NoError(t, stream.Close())

	stdout, stderr, err := readExecStreams(context.Background(), stream, stream, 16)

	require.NoError(t, err, "capping must not surface as a read failure")
	require.Equal(t, "abcdefghijklmnop", stdout.String(),
		"the buffer must stop growing at the cap, keeping the prefix")
	require.Empty(t, stderr.String())
}

// The cap applies per stream: stdout and stderr are demultiplexed into separate
// buffers, each bounded independently.
func TestReadExecStreamsCapsEachStream(t *testing.T) {
	stream := newFramedStream("AAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBB") // 16 bytes each
	require.NoError(t, stream.Close())

	stdout, stderr, err := readExecStreams(context.Background(), stream, stream, 8)

	require.NoError(t, err)
	require.Equal(t, "AAAAAAAA", stdout.String())
	require.Equal(t, "BBBBBBBB", stderr.String())
}

// A stream under the cap is returned whole — the cap must not truncate a
// legitimate payload.
func TestReadExecStreamsDoesNotCapUnderLimit(t *testing.T) {
	stream := newFramedStream("hello", "world")
	require.NoError(t, stream.Close())

	stdout, stderr, err := readExecStreams(context.Background(), stream, stream, 1024)

	require.NoError(t, err)
	require.Equal(t, "hello", stdout.String())
	require.Equal(t, "world", stderr.String())
}
