package monitor

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/sensor"
)

// ── Registry tests ────────────────────────────────────────────────────────────

func TestRegisterAndGet(t *testing.T) {
	b := &stubBackend{name: "stub-test"}
	Register(b)

	got, err := Get("stub-test")
	require.NoError(t, err)
	require.Equal(t, "stub-test", got.Name())
}

func TestGet_UnknownBackend(t *testing.T) {
	_, err := Get("does-not-exist-xyz")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does-not-exist-xyz")
}

func TestList_IncludesRegistered(t *testing.T) {
	Register(&stubBackend{name: "list-test-a"})
	Register(&stubBackend{name: "list-test-b"})

	names := List()
	require.Contains(t, names, "list-test-a")
	require.Contains(t, names, "list-test-b")
}

func TestTraceeBackendRegistered(t *testing.T) {
	// TraceeBackend registers itself via init() in tracee.go.
	_, err := Get("tracee")
	require.NoError(t, err)
}

// ── Collector tests ───────────────────────────────────────────────────────────

func TestCollector_DrainsSingleChannel(t *testing.T) {
	sink := &captureSink{}
	c := NewCollector(sink)

	ch := make(chan sensor.Event, 3)
	ch <- sensor.Event{NodeID: "node_attack", PID: 1}
	ch <- sensor.Event{NodeID: "node_web", PID: 2}
	ch <- sensor.Event{NodeID: "node_db", PID: 3}
	close(ch)

	c.Run(context.Background(), ch)

	require.Len(t, sink.events, 3)
	require.Equal(t, "node_attack", sink.events[0].NodeID)
	require.Equal(t, "node_db", sink.events[2].NodeID)
}

func TestCollector_DrainsMultipleChannels(t *testing.T) {
	sink := &captureSink{}
	c := NewCollector(sink)

	ch1 := make(chan sensor.Event, 2)
	ch1 <- sensor.Event{NodeID: "node_attack", PID: 10}
	ch1 <- sensor.Event{NodeID: "node_attack", PID: 11}
	close(ch1)

	ch2 := make(chan sensor.Event, 1)
	ch2 <- sensor.Event{NodeID: "node_web", PID: 20}
	close(ch2)

	c.Run(context.Background(), ch1, ch2)

	require.Len(t, sink.events, 3)
}

func TestCollector_CancelledContext(t *testing.T) {
	sink := &captureSink{}
	c := NewCollector(sink)

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan sensor.Event) // unbuffered, nothing will be sent
	cancel()

	c.Run(ctx, ch) // should return quickly
	require.Empty(t, sink.events)
}

// ── Target / Config zero-value safety ─────────────────────────────────────────

func TestTargetHandleNilSafe(t *testing.T) {
	tgt := Target{NodeID: "node", Substrate: "docker", Handle: nil}
	require.Equal(t, "", tgt.Handle["container_id"])
}

// ── Stubs ─────────────────────────────────────────────────────────────────────

type stubBackend struct{ name string }

func (s *stubBackend) Name() string { return s.name }
func (s *stubBackend) Start(_ context.Context, _ []Target, _ Config) (<-chan sensor.Event, error) {
	ch := make(chan sensor.Event)
	close(ch)
	return ch, nil
}
func (s *stubBackend) Stop(_ context.Context) error { return nil }

type captureSink struct {
	mu     sync.Mutex
	events []sensor.Event
}

func (c *captureSink) Write(e sensor.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}
func (c *captureSink) Close() error { return nil }
