package sensor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubLabeler labels events whose CgroupID == 999 as session "test-session".
type stubLabeler struct{}

func (s *stubLabeler) Annotate(e *Event) {
	if e.CgroupID == 999 {
		e.SessionID = "test-session"
		e.IsAttack = true
	}
}

const execveJSON = `{"timestamp":1000000,"hostProcessId":1234,"hostParentProcessId":100,"cgroupId":999,"processName":"nmap","eventName":"execve","args":[{"name":"pathname","type":"const char*","value":"/usr/bin/nmap"}]}`

const forkJSON = `{"timestamp":999000,"hostProcessId":100,"hostParentProcessId":50,"cgroupId":0,"processName":"bash","eventName":"clone","returnValue":1234,"args":[]}`

func TestMockBackendEventParsing(t *testing.T) {
	lines := []string{forkJSON, execveJSON}
	mb := NewMockBackend(lines, &stubLabeler{})

	ch, err := mb.Start(context.Background(), "node_a", "container-123")
	require.NoError(t, err)

	events := drainChan(ch)
	require.Len(t, events, 2)

	// Second event is execve with session label.
	execveEv := events[1]
	require.Equal(t, "execve", execveEv.Name)
	require.Equal(t, "test-session", execveEv.SessionID)
	require.True(t, execveEv.IsAttack)
	require.Equal(t, uint64(999), execveEv.CgroupID)
	require.Equal(t, "syscall", execveEv.Type)
	require.Equal(t, "/usr/bin/nmap", execveEv.Args["pathname"])
}

func TestMockBackendNoSession(t *testing.T) {
	noSessionJSON := `{"timestamp":2000000,"hostProcessId":5678,"hostParentProcessId":100,"cgroupId":1234,"processName":"ls","eventName":"execve","args":[{"name":"pathname","type":"const char*","value":"/bin/ls"}]}`
	mb := NewMockBackend([]string{noSessionJSON}, &stubLabeler{})
	ch, err := mb.Start(context.Background(), "node_a", "c1")
	require.NoError(t, err)
	events := drainChan(ch)
	require.Len(t, events, 1)
	require.Empty(t, events[0].SessionID)
	require.False(t, events[0].IsAttack)
}

func TestProcessTreeBuilder(t *testing.T) {
	b := NewProcessTreeBuilder(1)

	// Simulate: pid 1 forks pid 100 (bash), then pid 100 forks pid 200 (nmap).
	b.Feed(map[string]any{
		"eventName": "clone", "hostProcessId": float64(1),
		"hostParentProcessId": float64(0), "processName": "node-init",
		"returnValue": float64(100),
	})
	b.Feed(map[string]any{
		"eventName": "execve", "hostProcessId": float64(100),
		"hostParentProcessId": float64(1), "processName": "bash",
	})
	b.Feed(map[string]any{
		"eventName": "clone", "hostProcessId": float64(100),
		"hostParentProcessId": float64(1), "processName": "bash",
		"returnValue": float64(200),
	})
	b.Feed(map[string]any{
		"eventName": "execve", "hostProcessId": float64(200),
		"hostParentProcessId": float64(100), "processName": "nmap",
	})

	chain := b.Ancestry(200)
	require.Equal(t, []string{"node-init", "bash", "nmap"}, chain)
}

func TestProcessTreeCycleGuard(t *testing.T) {
	b := &ProcessTreeBuilder{procs: map[int]ProcInfo{
		1: {Comm: "a", PPID: 2},
		2: {Comm: "b", PPID: 1}, // cycle
	}}
	chain := b.Ancestry(1)
	require.NotEmpty(t, chain)
	require.LessOrEqual(t, len(chain), 32)
}

func drainChan(ch <-chan Event) []Event {
	var out []Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}
